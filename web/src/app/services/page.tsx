'use client'

import { useEffect, useState } from 'react'
import { useRouter } from 'next/navigation'
import dynamic from 'next/dynamic'
import { api } from '@/lib/api'
import type { GraphStats, RepoStats } from '@/lib/types'

const ServiceGraph = dynamic(() => import('@/components/graph/ServiceGraph'), {
  ssr: false,
  loading: () => (
    <div className="h-[400px] w-full rounded-lg border border-zinc-800 bg-zinc-950 flex items-center justify-center text-zinc-600 text-sm">
      Loading graph...
    </div>
  ),
})
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Loader2, ArrowRight, Globe, Key, Server, MessageSquare, Database, Zap, Package } from 'lucide-react'

interface Contract {
  id: string
  type: string
  role: string
  file_path: string
  line: number
  meta?: Record<string, string>
}

interface RepoContracts {
  contracts: Record<string, Contract[]>
  total: number
}

interface ServiceEdge {
  from: string
  to: string
  contracts: string[]
  type: string // 'env' | 'http' | 'grpc' | 'topic' etc.
}

interface ServiceNode {
  name: string
  stats?: RepoStats
  providers: number
  consumers: number
  contractTypes: string[]
}

const TYPE_ICONS: Record<string, React.ReactNode> = {
  http: <Globe className="h-3 w-3" />,
  grpc: <Server className="h-3 w-3" />,
  graphql: <Database className="h-3 w-3" />,
  topic: <MessageSquare className="h-3 w-3" />,
  ws: <Zap className="h-3 w-3" />,
  env: <Key className="h-3 w-3" />,
  dependency: <Package className="h-3 w-3" />,
}

const EDGE_COLORS: Record<string, string> = {
  http: '#7aa2f7',
  grpc: '#bb9af7',
  graphql: '#f7768e',
  topic: '#9ece6a',
  ws: '#e0af68',
  env: '#ff9e64',
  dependency: '#73daca',
  shared_infra: '#565f89',
}

const NODE_COLORS = [
  '#7aa2f7', '#9ece6a', '#e0af68', '#bb9af7', '#73daca',
  '#f7768e', '#ff9e64', '#7dcfff', '#c0caf5', '#a9b1d6',
]

export default function ServicesPage() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [services, setServices] = useState<ServiceNode[]>([])
  const [edges, setEdges] = useState<ServiceEdge[]>([])
  const [stats, setStats] = useState<GraphStats | null>(null)
  const router = useRouter()

  useEffect(() => {
    async function load() {
      try {
        setLoading(true)
        const [contractsText, statsData] = await Promise.all([
          api.callTool('get_contracts', {}),
          api.graphStats(),
        ])
        setStats(statsData)

        const parsed = JSON.parse(contractsText)
        const byRepo = parsed.by_repo as Record<string, RepoContracts> | undefined

        if (!byRepo || Object.keys(byRepo).length === 0) {
          setServices([])
          setEdges([])
          setError(null)
          return
        }

        // Build service nodes
        const serviceMap = new Map<string, ServiceNode>()
        for (const [repo, info] of Object.entries(byRepo)) {
          const repoStats = statsData.per_repo?.[repo]
          let providers = 0
          let consumers = 0
          const types = new Set<string>()
          for (const [typ, contracts] of Object.entries(info.contracts || {})) {
            types.add(typ)
            for (const c of contracts) {
              if (c.role === 'provider') providers++
              else consumers++
            }
          }
          serviceMap.set(repo, {
            name: repo,
            stats: repoStats,
            providers,
            consumers,
            contractTypes: Array.from(types),
          })
        }

        // Also add repos from stats that have no contracts (they're still services)
        if (statsData.per_repo) {
          for (const [repo, repoStats] of Object.entries(statsData.per_repo)) {
            if (!serviceMap.has(repo)) {
              serviceMap.set(repo, {
                name: repo,
                stats: repoStats as RepoStats,
                providers: 0,
                consumers: 0,
                contractTypes: [],
              })
            }
          }
        }

        // Build contract_id → {providers: Set<repo>, consumers: Set<repo>}
        const contractMap = new Map<string, { providers: Set<string>; consumers: Set<string>; type: string }>()
        for (const [repo, info] of Object.entries(byRepo)) {
          for (const [typ, contracts] of Object.entries(info.contracts || {})) {
            for (const c of contracts) {
              if (!contractMap.has(c.id)) {
                contractMap.set(c.id, { providers: new Set(), consumers: new Set(), type: typ })
              }
              const entry = contractMap.get(c.id)!
              if (c.role === 'provider') entry.providers.add(repo)
              else entry.consumers.add(repo)
            }
          }
        }

        // Build edges from dependency contracts: consumer repo → target_repo
        const edgeMap = new Map<string, ServiceEdge>()
        for (const [repo, info] of Object.entries(byRepo)) {
          for (const c of info.contracts?.dependency || []) {
            const targetRepo = (c.meta as Record<string, string>)?.target_repo
            if (targetRepo && targetRepo !== repo) {
              const key = `${repo}→${targetRepo}::dependency`
              if (!edgeMap.has(key)) {
                edgeMap.set(key, { from: repo, to: targetRepo, contracts: [], type: 'dependency' })
              }
              const module = (c.meta as Record<string, string>)?.module || c.id
              edgeMap.get(key)!.contracts.push(module)
            }
          }
        }

        // Build edges from other contract types: provider_repo → consumer_repo
        for (const [cid, info] of contractMap.entries()) {
          if (info.type === 'dependency') continue // already handled above
          // Cross-repo: provider in repo A, consumer in repo B
          for (const pRepo of info.providers) {
            for (const cRepo of info.consumers) {
              if (pRepo !== cRepo) {
                const key = `${pRepo}→${cRepo}::${info.type}`
                if (!edgeMap.has(key)) {
                  edgeMap.set(key, { from: pRepo, to: cRepo, contracts: [], type: info.type })
                }
                edgeMap.get(key)!.contracts.push(cid)
              }
            }
          }

          // Shared usage: same contract consumed/provided by multiple repos = shared infra
          const allRepos = new Set([...info.providers, ...info.consumers])
          if (allRepos.size > 1 && info.type === 'env') {
            const repos = Array.from(allRepos).sort()
            for (let i = 0; i < repos.length; i++) {
              for (let j = i + 1; j < repos.length; j++) {
                const key = `${repos[i]}↔${repos[j]}::shared_infra`
                if (!edgeMap.has(key)) {
                  edgeMap.set(key, { from: repos[i], to: repos[j], contracts: [], type: 'shared_infra' })
                }
                const edge = edgeMap.get(key)!
                if (!edge.contracts.includes(cid)) {
                  edge.contracts.push(cid)
                }
              }
            }
          }
        }

        setServices(Array.from(serviceMap.values()).sort((a, b) => (b.stats?.total_nodes || 0) - (a.stats?.total_nodes || 0)))
        setEdges(Array.from(edgeMap.values()).sort((a, b) => b.contracts.length - a.contracts.length))
        setError(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load')
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Loader2 className="h-6 w-6 animate-spin text-zinc-500" />
        <span className="ml-2 text-zinc-500">Analyzing service dependencies...</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center">
        <Card className="max-w-md bg-red-950/30 border-red-800">
          <CardContent className="p-6 text-center text-red-300">{error}</CardContent>
        </Card>
      </div>
    )
  }

  const directEdges = edges.filter(e => e.type !== 'shared_infra')
  const sharedEdges = edges.filter(e => e.type === 'shared_infra')

  // Prepare data for the visual graph
  const graphNodes = services.map(s => ({
    name: s.name,
    nodes: s.stats?.total_nodes || 0,
    contractTypes: s.contractTypes,
    provides: s.providers,
    consumes: s.consumers,
  }))

  function handleSelectRepo(repo: string) {
    // Navigate to the graph page filtered to this repo
    router.push(`/graph?repo=${encodeURIComponent(repo)}`)
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-zinc-100">Service Map</h1>
        <p className="text-sm text-zinc-500">
          Repository-level dependency graph based on detected API contracts
        </p>
      </div>

      {/* Summary */}
      <div className="grid gap-4 sm:grid-cols-3">
        <Card className="border-zinc-800 bg-zinc-900">
          <CardContent className="p-4 text-center">
            <div className="text-2xl font-bold text-zinc-100">{services.length}</div>
            <div className="text-xs text-zinc-500">Services</div>
          </CardContent>
        </Card>
        <Card className="border-zinc-800 bg-zinc-900">
          <CardContent className="p-4 text-center">
            <div className="text-2xl font-bold text-blue-400">{directEdges.length}</div>
            <div className="text-xs text-zinc-500">Direct Dependencies</div>
          </CardContent>
        </Card>
        <Card className="border-zinc-800 bg-zinc-900">
          <CardContent className="p-4 text-center">
            <div className="text-2xl font-bold text-orange-400">{sharedEdges.length}</div>
            <div className="text-xs text-zinc-500">Shared Infrastructure</div>
          </CardContent>
        </Card>
      </div>

      {/* Service dependency graph */}
      {services.length > 1 && (edges.length > 0) && (
        <div>
          <h2 className="mb-3 text-sm font-semibold text-zinc-300">Dependency Graph</h2>
          <p className="mb-2 text-xs text-zinc-600">Click a service to explore its internal graph</p>
          <ServiceGraph
            services={graphNodes}
            edges={edges}
            onSelectRepo={handleSelectRepo}
          />
        </div>
      )}

      {/* Service nodes */}
      <div>
        <h2 className="mb-3 text-sm font-semibold text-zinc-300">Services</h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {services.map((svc, i) => (
            <Card key={svc.name} className="border-zinc-800 bg-zinc-900">
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-2 text-sm">
                  <span
                    className="h-3 w-3 rounded-full shrink-0"
                    style={{ backgroundColor: NODE_COLORS[i % NODE_COLORS.length] }}
                  />
                  <span className="font-mono text-zinc-100">{svc.name}</span>
                  {svc.stats && (
                    <span className="ml-auto text-[10px] text-zinc-600">
                      {svc.stats.total_nodes.toLocaleString()} nodes
                    </span>
                  )}
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-2">
                <div className="flex flex-wrap gap-1">
                  {svc.contractTypes.map(t => (
                    <Badge
                      key={t}
                      variant="outline"
                      className="text-[10px] px-1.5 py-0 gap-1"
                      style={{ borderColor: `${EDGE_COLORS[t] || '#6b7280'}60`, color: EDGE_COLORS[t] || '#6b7280' }}
                    >
                      {TYPE_ICONS[t]}
                      {t}
                    </Badge>
                  ))}
                  {svc.contractTypes.length === 0 && (
                    <span className="text-[10px] text-zinc-700">no contracts detected</span>
                  )}
                </div>
                <div className="flex gap-3 text-[10px] text-zinc-500">
                  {svc.providers > 0 && <span className="text-green-500">{svc.providers} provides</span>}
                  {svc.consumers > 0 && <span className="text-blue-400">{svc.consumers} consumes</span>}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      </div>

      {/* Direct dependency edges */}
      {directEdges.length > 0 && (
        <div>
          <h2 className="mb-3 text-sm font-semibold text-zinc-300">Direct Dependencies</h2>
          <CardDescription className="mb-3 text-zinc-500">
            Service A provides a contract that Service B consumes
          </CardDescription>
          <div className="space-y-2">
            {directEdges.map((edge, i) => (
              <Card key={i} className="border-zinc-800 bg-zinc-900">
                <CardContent className="flex items-center gap-3 p-3">
                  <span className="font-mono text-sm text-green-400">{edge.from}</span>
                  <ArrowRight className="h-4 w-4 text-zinc-600 shrink-0" />
                  <span className="font-mono text-sm text-blue-400">{edge.to}</span>
                  <Badge
                    variant="outline"
                    className="ml-auto text-[10px] gap-1"
                    style={{ borderColor: `${EDGE_COLORS[edge.type] || '#6b7280'}60`, color: EDGE_COLORS[edge.type] || '#6b7280' }}
                  >
                    {TYPE_ICONS[edge.type]}
                    {edge.type}
                  </Badge>
                  <span className="text-xs text-zinc-500">{edge.contracts.length} contract{edge.contracts.length !== 1 ? 's' : ''}</span>
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      )}

      {/* Shared infrastructure */}
      {sharedEdges.length > 0 && (
        <div>
          <h2 className="mb-3 text-sm font-semibold text-zinc-300">Shared Infrastructure</h2>
          <CardDescription className="mb-3 text-zinc-500">
            Services using the same environment variables or configuration
          </CardDescription>
          <div className="space-y-2">
            {sharedEdges.map((edge, i) => (
              <Card key={i} className="border-zinc-800 bg-zinc-900">
                <CardContent className="p-3">
                  <div className="flex items-center gap-3">
                    <span className="font-mono text-sm text-zinc-300">{edge.from}</span>
                    <span className="text-zinc-600">↔</span>
                    <span className="font-mono text-sm text-zinc-300">{edge.to}</span>
                    <span className="ml-auto text-xs text-zinc-500">
                      {edge.contracts.length} shared var{edge.contracts.length !== 1 ? 's' : ''}
                    </span>
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {edge.contracts.slice(0, 6).map(cid => (
                      <code key={cid} className="text-[10px] text-zinc-600 bg-zinc-800 px-1 rounded">
                        {cid.replace('env::', '')}
                      </code>
                    ))}
                    {edge.contracts.length > 6 && (
                      <span className="text-[10px] text-zinc-600">+{edge.contracts.length - 6} more</span>
                    )}
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      )}

      {directEdges.length === 0 && sharedEdges.length === 0 && services.length > 0 && (
        <Card className="border-zinc-800 bg-zinc-900">
          <CardContent className="py-8 text-center text-sm text-zinc-500">
            No cross-repo contract connections detected yet.
            Services may communicate through patterns not yet recognized by the contract extractors (NATS, custom RPC, etc.).
          </CardContent>
        </Card>
      )}
    </div>
  )
}

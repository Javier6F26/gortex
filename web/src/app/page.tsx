'use client'

import { useEffect, useState } from 'react'
import { Activity, Box, GitBranch, Clock, CheckCircle } from 'lucide-react'
import {
  PieChart,
  Pie,
  Cell,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'
import { api } from '@/lib/api'
import { useStore } from '@/lib/store'
import { NODE_COLORS, LANGUAGE_COLORS } from '@/lib/colors'
import type { HealthResponse, GraphStats, RepoStats, NodeKind } from '@/lib/types'
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'

function formatUptime(seconds: number): string {
  if (seconds < 60) return `${Math.floor(seconds)}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return `${h}h ${m}m`
}

export default function DashboardPage() {
  const { setHealth, setStats, setConnected } = useStore()
  const [health, setLocalHealth] = useState<HealthResponse | null>(null)
  const [stats, setLocalStats] = useState<GraphStats | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let mounted = true

    async function fetchData() {
      try {
        // Use graphStats (MCP tool) instead of /stats endpoint
        // because it includes per_repo breakdown in multi-repo mode
        const [h, s] = await Promise.all([api.health(), api.graphStats()])
        if (!mounted) return
        setLocalHealth(h)
        setLocalStats(s)
        setHealth(h)
        setStats(s)
        setConnected(true)
        setError(null)
      } catch (err) {
        if (!mounted) return
        setConnected(false)
        setError(err instanceof Error ? err.message : 'Connection failed')
      }
    }

    fetchData()
    const interval = setInterval(fetchData, 10_000)
    return () => {
      mounted = false
      clearInterval(interval)
    }
  }, [setHealth, setStats, setConnected])

  const languageData = stats?.by_language
    ? Object.entries(stats.by_language).map(([name, value]) => ({
        name,
        value,
        color: LANGUAGE_COLORS[name] || '#6b7280',
      }))
    : []

  const kindData = stats?.by_kind
    ? Object.entries(stats.by_kind).map(([name, value]) => ({
        name,
        value,
        fill: NODE_COLORS[name as NodeKind] || '#6b7280',
      }))
    : []

  if (error && !health) {
    return (
      <div className="flex h-full items-center justify-center">
        <Card className="w-96 border-zinc-800 bg-zinc-900">
          <CardHeader>
            <CardTitle className="text-red-400">Connection Error</CardTitle>
            <CardDescription>{error}</CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-zinc-400">
              Make sure the Gortex bridge is running on the configured port.
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-zinc-100">Dashboard</h1>
        <p className="text-sm text-zinc-500">
          Gortex code intelligence overview
        </p>
      </div>

      {/* Top stat cards */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {/* Health */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader className="pb-2">
            <CardDescription className="text-zinc-500">Status</CardDescription>
            <CardTitle className="flex items-center gap-2 text-zinc-100">
              <Activity className="h-4 w-4 text-emerald-400" />
              {health?.status ?? '---'}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2">
              {health?.indexed ? (
                <Badge variant="secondary" className="bg-emerald-500/10 text-emerald-400 border-emerald-500/20">
                  Indexed
                </Badge>
              ) : (
                <Badge variant="secondary" className="bg-zinc-800 text-zinc-400">
                  Not indexed
                </Badge>
              )}
              {health?.version && (
                <span className="text-xs text-zinc-500">v{health.version}</span>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Uptime */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader className="pb-2">
            <CardDescription className="text-zinc-500">Uptime</CardDescription>
            <CardTitle className="flex items-center gap-2 text-zinc-100">
              <Clock className="h-4 w-4 text-blue-400" />
              {health ? formatUptime(health.uptime_seconds) : '---'}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xs text-zinc-500">
              {health
                ? `${Math.floor(health.uptime_seconds)}s total`
                : 'Waiting for connection'}
            </p>
          </CardContent>
        </Card>

        {/* Nodes */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader className="pb-2">
            <CardDescription className="text-zinc-500">
              Total Nodes
            </CardDescription>
            <CardTitle className="flex items-center gap-2 text-zinc-100">
              <Box className="h-4 w-4 text-purple-400" />
              {stats?.total_nodes?.toLocaleString() ?? '---'}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xs text-zinc-500">
              {stats?.by_kind
                ? `${Object.keys(stats.by_kind).length} kinds`
                : 'No data'}
            </p>
          </CardContent>
        </Card>

        {/* Edges */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader className="pb-2">
            <CardDescription className="text-zinc-500">
              Total Edges
            </CardDescription>
            <CardTitle className="flex items-center gap-2 text-zinc-100">
              <GitBranch className="h-4 w-4 text-orange-400" />
              {stats?.total_edges?.toLocaleString() ?? '---'}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xs text-zinc-500">
              {stats?.total_nodes && stats?.total_edges
                ? `${(stats.total_edges / stats.total_nodes).toFixed(1)} avg per node`
                : 'No data'}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Charts */}
      <div className="grid gap-4 lg:grid-cols-2">
        {/* Language breakdown */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader>
            <CardTitle className="text-zinc-100">Languages</CardTitle>
            <CardDescription className="text-zinc-500">
              Node distribution by language
            </CardDescription>
          </CardHeader>
          <CardContent>
            {languageData.length > 0 ? (
              <div className="flex items-center gap-6">
                <ResponsiveContainer width="100%" height={220}>
                  <PieChart>
                    <Pie
                      data={languageData}
                      cx="50%"
                      cy="50%"
                      innerRadius={50}
                      outerRadius={80}
                      paddingAngle={2}
                      dataKey="value"
                    >
                      {languageData.map((entry, i) => (
                        <Cell key={i} fill={entry.color} />
                      ))}
                    </Pie>
                    <Tooltip
                      contentStyle={{
                        backgroundColor: '#18181b',
                        border: '1px solid #27272a',
                        borderRadius: '6px',
                        fontSize: '12px',
                        color: '#e4e4e7',
                      }}
                    />
                  </PieChart>
                </ResponsiveContainer>
                <div className="space-y-1.5 text-xs">
                  {languageData.map((entry) => (
                    <div key={entry.name} className="flex items-center gap-2">
                      <span
                        className="h-2.5 w-2.5 rounded-full shrink-0"
                        style={{ backgroundColor: entry.color }}
                      />
                      <span className="text-zinc-300">{entry.name}</span>
                      <span className="ml-auto text-zinc-500">
                        {entry.value}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            ) : (
              <p className="py-8 text-center text-sm text-zinc-600">
                No language data available
              </p>
            )}
          </CardContent>
        </Card>

        {/* Node kind breakdown */}
        <Card className="border-zinc-800 bg-zinc-900">
          <CardHeader>
            <CardTitle className="text-zinc-100">Node Kinds</CardTitle>
            <CardDescription className="text-zinc-500">
              Node distribution by kind
            </CardDescription>
          </CardHeader>
          <CardContent>
            {kindData.length > 0 ? (
              <ResponsiveContainer width="100%" height={220}>
                <BarChart data={kindData} layout="vertical">
                  <XAxis
                    type="number"
                    tick={{ fill: '#71717a', fontSize: 11 }}
                    axisLine={{ stroke: '#27272a' }}
                    tickLine={false}
                  />
                  <YAxis
                    type="category"
                    dataKey="name"
                    width={70}
                    tick={{ fill: '#a1a1aa', fontSize: 11 }}
                    axisLine={false}
                    tickLine={false}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#18181b',
                      border: '1px solid #27272a',
                      borderRadius: '6px',
                      fontSize: '12px',
                      color: '#e4e4e7',
                    }}
                    cursor={{ fill: 'rgba(255,255,255,0.03)' }}
                  />
                  <Bar dataKey="value" radius={[0, 4, 4, 0]}>
                    {kindData.map((entry, i) => (
                      <Cell key={i} fill={entry.fill} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <p className="py-8 text-center text-sm text-zinc-600">
                No kind data available
              </p>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Per-repo breakdown (multi-repo mode) */}
      {stats?.per_repo && Object.keys(stats.per_repo).length > 1 && (
        <div className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold text-zinc-100">Repositories</h2>
            <p className="text-sm text-zinc-500">
              {Object.keys(stats.per_repo).length} repositories indexed
            </p>
          </div>

          <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-3">
            {Object.entries(stats.per_repo)
              .sort(([, a], [, b]) => b.total_nodes - a.total_nodes)
              .map(([name, repo]) => {
                const r = repo as RepoStats
                // Top 3 languages for this repo
                const topLangs = Object.entries(r.by_language || {})
                  .sort(([, a], [, b]) => b - a)
                  .slice(0, 4)
                // Meaningful kinds (skip file, import)
                const codeKinds = Object.entries(r.by_kind || {})
                  .filter(([k]) => k !== 'file' && k !== 'import' && k !== 'package')
                  .sort(([, a], [, b]) => b - a)

                return (
                  <Card key={name} className="border-zinc-800 bg-zinc-900">
                    <CardHeader className="pb-2">
                      <CardTitle className="flex items-center justify-between text-zinc-100">
                        <span className="font-mono text-sm">{name}</span>
                        <Badge variant="secondary" className="bg-zinc-800 text-zinc-400 text-[10px]">
                          {r.total_nodes.toLocaleString()} nodes
                        </Badge>
                      </CardTitle>
                    </CardHeader>
                    <CardContent className="space-y-3">
                      {/* Node/edge summary */}
                      <div className="flex gap-4 text-xs text-zinc-500">
                        <span>{r.total_edges.toLocaleString()} edges</span>
                        <span>{(r.total_edges / Math.max(r.total_nodes, 1)).toFixed(1)} avg/node</span>
                      </div>

                      {/* Symbol breakdown */}
                      <div className="flex flex-wrap gap-1.5">
                        {codeKinds.map(([kind, count]) => (
                          <span
                            key={kind}
                            className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px]"
                            style={{
                              backgroundColor: `${NODE_COLORS[kind as NodeKind] || '#6b7280'}15`,
                              color: NODE_COLORS[kind as NodeKind] || '#6b7280',
                            }}
                          >
                            <span
                              className="h-1.5 w-1.5 rounded-full"
                              style={{ backgroundColor: NODE_COLORS[kind as NodeKind] || '#6b7280' }}
                            />
                            {count} {kind}{count !== 1 ? 's' : ''}
                          </span>
                        ))}
                      </div>

                      {/* Top languages bar */}
                      {topLangs.length > 0 && (
                        <div className="space-y-1">
                          <div className="flex h-1.5 overflow-hidden rounded-full bg-zinc-800">
                            {topLangs.map(([lang, count]) => (
                              <div
                                key={lang}
                                className="h-full"
                                style={{
                                  width: `${(count / r.total_nodes) * 100}%`,
                                  backgroundColor: LANGUAGE_COLORS[lang] || '#6b7280',
                                }}
                                title={`${lang}: ${count}`}
                              />
                            ))}
                          </div>
                          <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-zinc-500">
                            {topLangs.map(([lang, count]) => (
                              <span key={lang} className="flex items-center gap-1">
                                <span
                                  className="h-1.5 w-1.5 rounded-full"
                                  style={{ backgroundColor: LANGUAGE_COLORS[lang] || '#6b7280' }}
                                />
                                {lang} {count}
                              </span>
                            ))}
                          </div>
                        </div>
                      )}
                    </CardContent>
                  </Card>
                )
              })}
          </div>
        </div>
      )}
    </div>
  )
}

'use client'

import { useEffect, useRef, useState } from 'react'
import dynamic from 'next/dynamic'
import { api } from '@/lib/api'
import { useStore } from '@/lib/store'
import type { GraphData, SubGraph } from '@/lib/types'
import GraphFilters from '@/components/graph/GraphFilters'
import NodeDetail from '@/components/graph/NodeDetail'
import { Loader2 } from 'lucide-react'

const GraphCanvas = dynamic(() => import('@/components/graph/GraphCanvas'), {
  ssr: false,
  loading: () => (
    <div className="flex h-full w-full items-center justify-center">
      <Loader2 className="h-6 w-6 animate-spin text-zinc-500" />
    </div>
  ),
})

export default function GraphExplorerPage() {
  const [fullGraphData, setFullGraphData] = useState<GraphData | null>(null)
  const [graphData, setGraphData] = useState<GraphData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [repos, setRepos] = useState<string[]>([])
  const [selectedRepo, setSelectedRepo] = useState<string>('all')

  const { selectedNodeId } = useStore()

  const fitCameraRef = useRef<(() => void) | null>(null)
  const relayoutRef = useRef<(() => void) | null>(null)

  // Fetch full graph once.
  useEffect(() => {
    let mounted = true

    async function fetchGraph() {
      try {
        setLoading(true)
        const data = await api.getGraph()
        if (!mounted) return

        // Detect repos from node repo_prefix fields.
        const repoSet = new Set<string>()
        for (const n of data.nodes) {
          if (n.repo_prefix) repoSet.add(n.repo_prefix)
        }
        const repoList = Array.from(repoSet).sort()
        setRepos(repoList)
        setFullGraphData(data)

        // Auto-select first repo if multi-repo and too many nodes.
        if (repoList.length > 1 && data.nodes.length > 5000) {
          setSelectedRepo(repoList[0])
        }

        setError(null)
      } catch (err) {
        if (!mounted) return
        setError(err instanceof Error ? err.message : 'Failed to load graph')
      } finally {
        if (mounted) setLoading(false)
      }
    }

    fetchGraph()
    return () => { mounted = false }
  }, [])

  // Filter graph by selected repo.
  useEffect(() => {
    if (!fullGraphData) return

    if (selectedRepo === 'all') {
      // For "all", filter heavy node types if too large.
      if (fullGraphData.nodes.length > 10000) {
        const filtered = fullGraphData.nodes.filter(n =>
          n.kind !== 'file' && n.kind !== 'import' && n.kind !== 'variable'
        )
        const ids = new Set(filtered.map(n => n.id))
        setGraphData({
          nodes: filtered,
          edges: fullGraphData.edges.filter(e => ids.has(e.from) && ids.has(e.to)),
          stats: fullGraphData.stats,
        })
      } else {
        setGraphData(fullGraphData)
      }
    } else {
      // Filter to selected repo only.
      const repoNodes = fullGraphData.nodes.filter(n =>
        n.repo_prefix === selectedRepo || n.file_path.startsWith(selectedRepo + '/')
      )
      const ids = new Set(repoNodes.map(n => n.id))
      setGraphData({
        nodes: repoNodes,
        edges: fullGraphData.edges.filter(e => ids.has(e.from) && ids.has(e.to)),
        stats: fullGraphData.stats,
      })
    }
  }, [fullGraphData, selectedRepo])

  function handleFitCamera() {
    fitCameraRef.current?.()
  }

  function handleRelayout() {
    relayoutRef.current?.()
  }

  const [clusterActive, setClusterActive] = useState(false)

  function handleFocusCluster(cluster: SubGraph) {
    if (!cluster.nodes || cluster.nodes.length === 0) return
    const clusterNodeIds = new Set(cluster.nodes.map(n => n.id))
    const clusterEdges = (fullGraphData?.edges || []).filter(
      e => clusterNodeIds.has(e.from) && clusterNodeIds.has(e.to)
    )
    setGraphData({
      nodes: cluster.nodes,
      edges: clusterEdges,
      stats: graphData?.stats || { total_nodes: 0, total_edges: 0, by_kind: {}, by_language: {} },
    })
    setClusterActive(true)
    // Re-layout with the smaller cluster graph
    setTimeout(() => relayoutRef.current?.(), 200)
  }

  function handleExitCluster() {
    setClusterActive(false)
    // Re-trigger the repo filter effect
    setSelectedRepo(prev => prev) // force re-render
    // Re-apply full graph
    if (fullGraphData) {
      if (selectedRepo === 'all') {
        if (fullGraphData.nodes.length > 10000) {
          const filtered = fullGraphData.nodes.filter(n =>
            n.kind !== 'file' && n.kind !== 'import' && n.kind !== 'variable'
          )
          const ids = new Set(filtered.map(n => n.id))
          setGraphData({
            nodes: filtered,
            edges: fullGraphData.edges.filter(e => ids.has(e.from) && ids.has(e.to)),
            stats: fullGraphData.stats,
          })
        } else {
          setGraphData(fullGraphData)
        }
      } else {
        const repoNodes = fullGraphData.nodes.filter(n =>
          n.repo_prefix === selectedRepo || n.file_path.startsWith(selectedRepo + '/')
        )
        const ids = new Set(repoNodes.map(n => n.id))
        setGraphData({
          nodes: repoNodes,
          edges: fullGraphData.edges.filter(e => ids.has(e.from) && ids.has(e.to)),
          stats: fullGraphData.stats,
        })
      }
    }
    setTimeout(() => {
      relayoutRef.current?.()
      setTimeout(() => fitCameraRef.current?.(), 500)
    }, 200)
  }

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="flex items-center gap-3 text-zinc-500">
          <Loader2 className="h-5 w-5 animate-spin" />
          <span className="text-sm">Loading graph data...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="max-w-sm rounded-lg border border-zinc-800 bg-zinc-900 p-6 text-center">
          <p className="mb-2 text-sm font-medium text-red-400">Failed to load graph</p>
          <p className="text-xs text-zinc-500">{error}</p>
        </div>
      </div>
    )
  }

  if (!graphData || graphData.nodes.length === 0) {
    return (
      <div className="flex h-full items-center justify-center">
        <p className="text-sm text-zinc-500">No graph data available. Make sure a repository is indexed.</p>
      </div>
    )
  }

  return (
    <div className="-m-6 flex h-[calc(100vh-theme(spacing.24))] overflow-hidden">
      <div className="flex w-60 shrink-0 flex-col border-r border-zinc-800 bg-zinc-900 overflow-y-auto">
        {/* Repo selector */}
        {repos.length > 1 && (
          <div className="p-3 border-b border-zinc-800">
            <label className="text-xs font-medium text-zinc-500 uppercase tracking-wider">Repository</label>
            <select
              value={selectedRepo}
              onChange={(e) => setSelectedRepo(e.target.value)}
              className="mt-1 w-full rounded bg-zinc-800 border border-zinc-700 text-sm text-zinc-200 px-2 py-1.5 focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              <option value="all">All Repos ({fullGraphData?.nodes.length || 0} nodes)</option>
              {repos.map(r => {
                const count = fullGraphData?.nodes.filter(n => n.repo_prefix === r).length || 0
                return <option key={r} value={r}>{r} ({count} nodes)</option>
              })}
            </select>
          </div>
        )}
        <GraphFilters
          nodeCount={graphData.nodes.length}
          edgeCount={graphData.edges.length}
          repos={repos}
          onFitCamera={handleFitCamera}
          onRelayout={handleRelayout}
        />
      </div>

      <div className="relative flex-1 overflow-hidden bg-zinc-950">
        {clusterActive && (
          <div className="absolute top-3 left-1/2 -translate-x-1/2 z-10 flex items-center gap-3 rounded-lg border border-zinc-700 bg-zinc-900/95 px-4 py-2 backdrop-blur shadow-lg">
            <span className="text-xs text-zinc-400">
              Cluster view — {graphData.nodes.length} nodes
            </span>
            <button
              onClick={handleExitCluster}
              className="rounded border border-zinc-600 bg-zinc-800 px-2.5 py-1 text-xs text-zinc-300 hover:bg-zinc-700 transition-colors"
            >
              Back to full graph
            </button>
          </div>
        )}
        <GraphCanvas
          nodes={graphData.nodes}
          edges={graphData.edges}
          fitCameraRef={fitCameraRef}
          relayoutRef={relayoutRef}
        />

        {/* Zoom controls overlay */}
        <div className="absolute bottom-4 right-4 flex flex-col gap-1">
          <button
            onClick={handleFitCamera}
            className="flex h-8 w-8 items-center justify-center rounded-lg border border-zinc-700 bg-zinc-900/90 text-xs font-medium text-zinc-300 backdrop-blur hover:bg-zinc-800"
            title="Fit to screen"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M4 8V4m0 0h4M4 4l5 5m11-1V4m0 0h-4m4 0l-5 5M4 16v4m0 0h4m-4 0l5-5m11 5v-4m0 4h-4m4 0l-5-5" />
            </svg>
          </button>
          <button
            onClick={() => {
              // Zoom in by decreasing camera ratio
              const canvas = document.querySelector('[data-sigma-mouse-container]') as HTMLElement | null
              if (canvas) {
                const event = new WheelEvent('wheel', { deltaY: -100, bubbles: true })
                canvas.dispatchEvent(event)
              }
            }}
            className="flex h-8 w-8 items-center justify-center rounded-lg border border-zinc-700 bg-zinc-900/90 text-lg font-medium text-zinc-300 backdrop-blur hover:bg-zinc-800"
            title="Zoom in"
          >
            +
          </button>
          <button
            onClick={() => {
              const canvas = document.querySelector('[data-sigma-mouse-container]') as HTMLElement | null
              if (canvas) {
                const event = new WheelEvent('wheel', { deltaY: 100, bubbles: true })
                canvas.dispatchEvent(event)
              }
            }}
            className="flex h-8 w-8 items-center justify-center rounded-lg border border-zinc-700 bg-zinc-900/90 text-lg font-medium text-zinc-300 backdrop-blur hover:bg-zinc-800"
            title="Zoom out"
          >
            -
          </button>
        </div>
      </div>

      {selectedNodeId && <NodeDetail onFocusCluster={handleFocusCluster} />}
    </div>
  )
}

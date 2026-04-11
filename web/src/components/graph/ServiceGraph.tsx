'use client'

import { useEffect, useRef, useCallback } from 'react'
import Graph from 'graphology'
import Sigma from 'sigma'
import { EdgeArrowProgram } from 'sigma/rendering'

interface ServiceNode {
  name: string
  nodes: number
  contractTypes: string[]
  provides: number
  consumes: number
}

interface ServiceEdge {
  from: string
  to: string
  contracts: string[]
  type: string
}

interface ServiceGraphProps {
  services: ServiceNode[]
  edges: ServiceEdge[]
  onSelectRepo?: (repo: string) => void
}

const NODE_COLORS = [
  '#7aa2f7', '#9ece6a', '#e0af68', '#bb9af7', '#73daca',
  '#f7768e', '#ff9e64', '#7dcfff', '#c0caf5', '#a9b1d6',
]

const EDGE_TYPE_COLORS: Record<string, string> = {
  dependency: '#73daca',
  http: '#7aa2f7',
  grpc: '#bb9af7',
  env: '#ff9e64',
  shared_infra: '#565f89',
  topic: '#9ece6a',
}

export default function ServiceGraph({ services, edges, onSelectRepo }: ServiceGraphProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const sigmaRef = useRef<Sigma | null>(null)

  const buildGraph = useCallback(() => {
    const graph = new Graph({ multi: true, type: 'directed' })

    for (let i = 0; i < services.length; i++) {
      const svc = services[i]
      // Size based on node count (log scale)
      // Flatten size range so large repos don't dominate visually
      const size = Math.max(8, Math.min(25, 6 + Math.log10(svc.nodes + 1) * 5))
      graph.addNode(svc.name, {
        label: `${svc.name} (${svc.nodes.toLocaleString()})`,
        size,
        color: NODE_COLORS[i % NODE_COLORS.length],
        x: 0,
        y: 0,
        nodeData: svc,
      })
    }

    // Apply circular layout as initial positions
    const nodeIds = graph.nodes()
    const n = nodeIds.length
    nodeIds.forEach((id, i) => {
      const angle = (2 * Math.PI * i) / n
      graph.setNodeAttribute(id, 'x', Math.cos(angle) * 300)
      graph.setNodeAttribute(id, 'y', Math.sin(angle) * 300)
    })

    for (const edge of edges) {
      if (!graph.hasNode(edge.from) || !graph.hasNode(edge.to)) continue
      try {
        graph.addEdge(edge.from, edge.to, {
          color: EDGE_TYPE_COLORS[edge.type] || '#3b4261',
          size: Math.max(1, Math.min(5, edge.contracts.length * 0.5)),
          type: 'arrow',
          edgeType: edge.type,
          label: `${edge.type} (${edge.contracts.length})`,
          contractCount: edge.contracts.length,
        })
      } catch {
        // skip duplicates
      }
    }

    return graph
  }, [services, edges])

  useEffect(() => {
    if (!containerRef.current || services.length === 0) return

    const graph = buildGraph()

    const sigma = new Sigma(graph, containerRef.current, {
      renderEdgeLabels: true,
      defaultEdgeType: 'arrow',
      edgeProgramClasses: { arrow: EdgeArrowProgram },
      defaultDrawNodeHover: () => {},
      labelFont: 'JetBrains Mono, monospace',
      labelSize: 14,
      labelWeight: '600',
      labelColor: { color: '#e4e4e7' },
      edgeLabelFont: 'Inter, sans-serif',
      edgeLabelSize: 10,
      edgeLabelColor: { color: '#71717a' },
      labelRenderedSizeThreshold: 0,
      stagePadding: 80,
    })

    sigmaRef.current = sigma

    // Click handler
    sigma.on('clickNode', ({ node }) => {
      onSelectRepo?.(node)
    })

    // Hover
    let hoveredNode: string | null = null
    sigma.on('enterNode', ({ node }) => { hoveredNode = node; sigma.refresh() })
    sigma.on('leaveNode', () => { hoveredNode = null; sigma.refresh() })

    // Reducers for hover highlight
    sigma.setSetting('nodeReducer', (nodeId, data) => {
      if (hoveredNode && hoveredNode !== nodeId) {
        const isNeighbor = graph.hasEdge(hoveredNode, nodeId) || graph.hasEdge(nodeId, hoveredNode)
        if (!isNeighbor) {
          const hex = (data.color as string) || '#6b7280'
          const r = parseInt(hex.slice(1, 3), 16)
          const g = parseInt(hex.slice(3, 5), 16)
          const b = parseInt(hex.slice(5, 7), 16)
          return { ...data, color: `rgba(${r},${g},${b},0.15)`, label: null }
        }
      }
      if (hoveredNode === nodeId) {
        return { ...data, highlighted: true, zIndex: 2 }
      }
      return data
    })

    sigma.setSetting('edgeReducer', (edge, data) => {
      if (hoveredNode) {
        const [source, target] = graph.extremities(edge)
        if (source !== hoveredNode && target !== hoveredNode) {
          return { ...data, hidden: true }
        }
        return { ...data, size: Math.max((data.size as number) || 1, 2) }
      }
      return data
    })

    // For small service graphs (< 20 nodes), circular layout is cleaner
    // than force-directed which tends to overshoot.
    sigma.getCamera().animatedReset({ duration: 300 })

    return () => {
      sigma.kill()
    }
  }, [buildGraph, onSelectRepo, services.length])

  return (
    <div
      ref={containerRef}
      className="h-[400px] w-full rounded-lg border border-zinc-800 bg-zinc-950"
      style={{ cursor: 'pointer' }}
    />
  )
}

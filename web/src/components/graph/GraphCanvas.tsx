'use client'

import { useEffect, useRef, useCallback } from 'react'
import Graph from 'graphology'
import Sigma from 'sigma'
import { EdgeArrowProgram } from 'sigma/rendering'
import FA2LayoutSupervisor from 'graphology-layout-forceatlas2/worker'
import { inferSettings } from 'graphology-layout-forceatlas2'
import type { GortexNode, GortexEdge, NodeKind, EdgeKind } from '@/lib/types'
import { NODE_COLORS, EDGE_COLORS } from '@/lib/colors'
import { useStore } from '@/lib/store'

interface GraphCanvasProps {
  nodes: GortexNode[]
  edges: GortexEdge[]
  fitCameraRef?: React.MutableRefObject<(() => void) | null>
  relayoutRef?: React.MutableRefObject<(() => void) | null>
}

export default function GraphCanvas({ nodes, edges, fitCameraRef, relayoutRef }: GraphCanvasProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const sigmaRef = useRef<Sigma | null>(null)
  const graphRef = useRef<Graph | null>(null)
  const layoutRef = useRef<FA2LayoutSupervisor | null>(null)
  const hoveredNodeRef = useRef<string | null>(null)

  const { visibleKinds, visibleRepos, hideTestFiles, hideImports, selectNode, setHoveredNode } = useStore()

  // Build graph from data
  const buildGraph = useCallback(() => {
    const graph = new Graph({ multi: true, type: 'directed' })

    const nodeIds = new Set<string>()
    for (const node of nodes) {
      if (nodeIds.has(node.id)) continue
      nodeIds.add(node.id)

      // Spread initial positions wide so repulsion has room to work
      graph.addNode(node.id, {
        x: (Math.random() - 0.5) * 1000,
        y: (Math.random() - 0.5) * 1000,
        label: node.name,
        size: 5,
        color: NODE_COLORS[node.kind as NodeKind] || '#6b7280',
        nodeKind: node.kind,
        filePath: node.file_path,
        repoPrefix: node.repo_prefix || '',
        hidden: false,
      })
    }

    for (const edge of edges) {
      if (!graph.hasNode(edge.from) || !graph.hasNode(edge.to)) continue
      try {
        graph.addEdge(edge.from, edge.to, {
          color: EDGE_COLORS[edge.kind as EdgeKind] || '#3b4261',
          size: 1,
          type: 'arrow',
          edgeKind: edge.kind,
          filePath: edge.file_path,
        })
      } catch {
        // skip duplicate edges in non-multi mode
      }
    }

    // Set node sizes based on degree (logarithmic)
    graph.forEachNode((nodeId) => {
      const degree = graph.degree(nodeId)
      const size = Math.min(20, Math.max(3, 3 + Math.log2(degree + 1) * 3))
      graph.setNodeAttribute(nodeId, 'size', size)
    })

    return graph
  }, [nodes, edges])

  // Start layout using a semantic-only graph for positioning.
  // We exclude 'defines' and 'imports' edges from layout calculation
  // so that communities form around call/reference patterns instead of
  // collapsing into a single blob around file nodes.
  const startLayout = useCallback((graph: Graph) => {
    if (layoutRef.current) {
      layoutRef.current.kill()
      layoutRef.current = null
    }

    // Build a layout-only graph with semantic edges only
    const layoutGraph = new Graph({ multi: true, type: 'directed' })
    const LAYOUT_SKIP_EDGES = new Set(['defines', 'imports'])

    graph.forEachNode((id, attrs) => {
      layoutGraph.addNode(id, { ...attrs })
    })
    graph.forEachEdge((_edge, attrs, source, target) => {
      const kind = attrs.edgeKind as string
      if (LAYOUT_SKIP_EDGES.has(kind)) return
      try {
        layoutGraph.addEdge(source, target, { ...attrs })
      } catch {
        // skip duplicates
      }
    })

    // Assign edge weights: cross-community edges are weaker (push clusters apart),
    // same-file edges are stronger (keep related symbols close).
    layoutGraph.forEachEdge((edge, attrs, source, target) => {
      const srcFile = layoutGraph.getNodeAttribute(source, 'filePath') as string
      const tgtFile = layoutGraph.getNodeAttribute(target, 'filePath') as string
      const kind = attrs.edgeKind as string

      let weight = 1
      if (srcFile && tgtFile && srcFile === tgtFile) {
        weight = 5 // same file — pull together strongly
      } else if (kind === 'member_of' || kind === 'implements') {
        weight = 3 // structural relationship
      } else if (kind === 'calls') {
        weight = 1 // normal call
      } else if (kind === 'references' || kind === 'instantiates') {
        weight = 0.5 // weaker reference
      }
      layoutGraph.setEdgeAttribute(edge, 'weight', weight)
    })

    const settings = inferSettings(layoutGraph)
    const layout = new FA2LayoutSupervisor(layoutGraph, {
      settings: {
        ...settings,
        barnesHutOptimize: layoutGraph.order > 500,
        barnesHutTheta: 0.5,
        slowDown: 3,
        gravity: 0.05,                  // very weak gravity → clusters drift apart
        scalingRatio: 30,               // strong repulsion → clear gaps between clusters
        strongGravityMode: false,
        edgeWeightInfluence: 1,         // respect the weights we assigned
        outboundAttractionDistribution: true, // hubs don't collapse their neighbors
      },
    })

    // Sync positions back: layoutGraph → display graph
    const syncPositions = () => {
      layoutGraph.forEachNode((id, attrs) => {
        if (graph.hasNode(id)) {
          graph.setNodeAttribute(id, 'x', attrs.x)
          graph.setNodeAttribute(id, 'y', attrs.y)
        }
      })
    }

    // Sync periodically while running
    const syncInterval = setInterval(syncPositions, 100)

    layout.start()
    layoutRef.current = layout

    // Auto-stop after 8 seconds — enough for strong repulsion to separate clusters
    setTimeout(() => {
      if (layout.isRunning()) {
        layout.stop()
      }
      syncPositions()
      clearInterval(syncInterval)
    }, 8000)

    return layout
  }, [])

  // Apply visibility filters via reducers
  useEffect(() => {
    const sigma = sigmaRef.current
    if (!sigma) return

    sigma.setSetting('nodeReducer', (nodeId, data) => {
      const kind = data.nodeKind as string
      const filePath = data.filePath as string
      const repoPrefix = data.repoPrefix as string

      let hidden = false

      // Repo filter (multi-repo mode)
      if (visibleRepos !== null && repoPrefix && !visibleRepos.has(repoPrefix)) {
        hidden = true
      }

      // Kind filter
      if (!visibleKinds.has(kind)) {
        hidden = true
      }

      // Test file filter
      if (hideTestFiles && filePath && /_test\.|\.test\.|\.spec\.|_test\.go/.test(filePath)) {
        hidden = true
      }

      // Import filter
      if (hideImports && kind === 'import') {
        hidden = true
      }

      // Hover highlight logic — dim non-neighbors
      const hovered = hoveredNodeRef.current
      if (hovered && !hidden) {
        const graph = graphRef.current
        if (graph && hovered !== nodeId) {
          const isNeighbor = graph.hasEdge(hovered, nodeId) || graph.hasEdge(nodeId, hovered)
          if (!isNeighbor) {
            // Dim the color: parse hex and set low opacity
            const hex = (data.color as string) || '#6b7280'
            const r = parseInt(hex.slice(1, 3), 16)
            const g = parseInt(hex.slice(3, 5), 16)
            const b = parseInt(hex.slice(5, 7), 16)
            return { ...data, hidden: false, color: `rgba(${r},${g},${b},0.08)`, label: null }
          }
          // Hovered node's neighbor — make slightly brighter/larger
          return { ...data, hidden: false, zIndex: 1 }
        }
        // The hovered node itself — highlight
        if (hovered === nodeId) {
          return { ...data, hidden: false, highlighted: true, zIndex: 2 }
        }
      }

      return { ...data, hidden }
    })

    sigma.setSetting('edgeReducer', (_edge, data) => {
      const hovered = hoveredNodeRef.current
      if (hovered) {
        const graph = graphRef.current
        if (graph) {
          const [source, target] = graph.extremities(_edge)
          if (source !== hovered && target !== hovered) {
            return { ...data, hidden: true }
          }
        }
      }
      return data
    })

    sigma.refresh()
  }, [visibleKinds, visibleRepos, hideTestFiles, hideImports])

  // Main setup effect
  useEffect(() => {
    if (!containerRef.current || nodes.length === 0) return

    const graph = buildGraph()
    graphRef.current = graph

    const sigma = new Sigma(graph, containerRef.current, {
      defaultEdgeType: 'arrow',
      edgeProgramClasses: {
        arrow: EdgeArrowProgram,
      },
      labelColor: { color: '#d4d4d8' },
      labelSize: 12,
      labelRenderedSizeThreshold: 8,
      defaultNodeColor: '#6b7280',
      defaultEdgeColor: '#3b4261',
      renderLabels: true,
      renderEdgeLabels: false,
      hideEdgesOnMove: true,
      hideLabelsOnMove: false,
      enableEdgeEvents: false,
      zIndex: true,
      allowInvalidContainer: true,
      // Disable the default hover highlight (bright white halo)
      defaultDrawNodeHover: () => {},
      nodeReducer: (nodeId, data) => {
        const kind = data.nodeKind as string
        const filePath = data.filePath as string

        let hidden = false
        const state = useStore.getState()

        if (!state.visibleKinds.has(kind)) hidden = true
        if (state.hideTestFiles && filePath && /_test\.|\.test\.|\.spec\.|_test\.go/.test(filePath)) hidden = true
        if (state.hideImports && kind === 'import') hidden = true

        return { ...data, hidden }
      },
    })

    sigmaRef.current = sigma

    // Click handler
    sigma.on('clickNode', ({ node }) => {
      const gNode = nodes.find(n => n.id === node) ?? null
      selectNode(node, gNode)
    })

    sigma.on('clickStage', () => {
      selectNode(null, null)
    })

    // Hover handler
    sigma.on('enterNode', ({ node }) => {
      hoveredNodeRef.current = node
      setHoveredNode(node)
      sigma.refresh()
    })

    sigma.on('leaveNode', () => {
      hoveredNodeRef.current = null
      setHoveredNode(null)
      sigma.refresh()
    })

    // Start ForceAtlas2 layout
    const layout = startLayout(graph)

    // Expose camera fit
    if (fitCameraRef) {
      fitCameraRef.current = () => {
        const camera = sigma.getCamera()
        camera.animatedReset({ duration: 300 })
      }
    }

    // Expose re-layout
    if (relayoutRef) {
      relayoutRef.current = () => {
        if (layout.isRunning()) {
          layout.stop()
        }
        startLayout(graph)
      }
    }

    return () => {
      if (layoutRef.current) {
        layoutRef.current.kill()
        layoutRef.current = null
      }
      sigma.kill()
      sigmaRef.current = null
      graphRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes, edges])

  return (
    <div
      ref={containerRef}
      className="h-full w-full"
      style={{ minHeight: '400px' }}
    />
  )
}

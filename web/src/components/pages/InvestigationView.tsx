'use client'

import { useEffect, useMemo, useState } from 'react'
import { Icon } from '@/components/primitives/Icon'
import { CaveatBadge } from '@/components/primitives/Caveat'
import {
  useContracts, useGuards, useProcesses, useProcessDetail,
  useActivity, useSymbolSource, useSymbol,
} from '@/lib/hooks'
import { useInspector } from '@/lib/inspector'

// Splits a node ID of the form "<repoPrefix>/<path>::<symbol>" into its
// parts so we can render a step without extra round-trips. Symbol-only
// IDs (e.g. "unresolved::OSTRACE") end up with an empty repo and path.
function parseStepId(id: string): { repo: string; path: string; symbol: string } {
  const symIdx = id.indexOf('::')
  const pathPart = symIdx >= 0 ? id.slice(0, symIdx) : id
  const symbol = symIdx >= 0 ? id.slice(symIdx + 2) : id
  const slashIdx = pathPart.indexOf('/')
  if (slashIdx >= 0) {
    return { repo: pathPart.slice(0, slashIdx), path: pathPart.slice(slashIdx + 1), symbol }
  }
  return { repo: '', path: pathPart, symbol }
}

// Hard cap on how many flow steps we render. Some flows (e.g. sqlite)
// have 800+ steps — listing them all would drown the tile and the UI
// doesn't gain much past the first few dozen.
const STEP_LIMIT = 40

export function InvestigationView() {
  const { data: processes, loading: procLoading } = useProcesses()
  const [selectedProc, setSelectedProc] = useState<string | null>(null)
  useEffect(() => {
    if (!selectedProc && processes && processes.length > 0) {
      setSelectedProc(processes[0].id)
    }
  }, [processes, selectedProc])

  const { data: detail } = useProcessDetail(selectedProc)
  const steps = useMemo(() => (detail?.steps ?? []).slice(0, STEP_LIMIT), [detail])

  const [stepIdx, setStepIdx] = useState(0)
  useEffect(() => { setStepIdx(0) }, [selectedProc])

  const selectedStepId = steps[stepIdx] ?? null
  const { data: source, loading: sourceLoading } = useSymbolSource(selectedStepId)
  const { data: node } = useSymbol(selectedStepId)

  // Feed the global Inspector with the selected step so the right
  // pane's callers/callees stay in sync with the flow trace here.
  const setInspector = useInspector((s) => s.setSym)
  useEffect(() => {
    if (!selectedStepId) return
    const parsed = parseStepId(selectedStepId)
    setInspector({
      id: selectedStepId,
      kind: (node?.kind as string) ?? 'function',
      name: node?.name || parsed.symbol,
      repo: node?.repo_prefix || parsed.repo,
      file: node?.file_path
        ? `${node.file_path}${node.start_line ? `:${node.start_line}` : ''}`
        : parsed.path,
      sig: (node?.meta?.signature as string) ?? '',
      callers: 0,
      callees: 0,
      community: '',
      caveats: [],
    })
  }, [selectedStepId, node, setInspector])

  const { data: activity } = useActivity(20)
  const { data: contracts } = useContracts()
  const { data: guards } = useGuards()

  const proc = processes?.find((p) => p.id === selectedProc) ?? processes?.[0]

  return (
    <>
      <div className="page-hd">
        <div>
          <div className="hstack" style={{ gap: 8, marginBottom: 4 }}>
            <Icon name="flask" size={14} />
            <span className="mono faint" style={{ fontSize: 11 }}>
              investigation · top process by score
            </span>
          </div>
          <h1>{proc?.name ?? (procLoading ? 'Loading flow…' : 'No processes discovered')}</h1>
          <div className="sub">
            {proc
              ? `${proc.crosses.length > 0 ? proc.crosses.join(' → ') : 'single repo'} · ${proc.steps} steps · score ${proc.score}`
              : 'Process detection runs after indexing — try re-indexing the repository.'}
          </div>
        </div>
        {processes && processes.length > 1 && (
          <div className="actions">
            <select
              value={selectedProc ?? ''}
              onChange={(e) => setSelectedProc(e.target.value)}
              className="btn"
              style={{ padding: '4px 8px' }}
            >
              {processes.slice(0, 20).map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </select>
          </div>
        )}
      </div>
      <div style={{ overflow: 'auto', flex: 1 }}>
        <div className="inv-grid">
          <div className="inv-tile inv-c-8">
            <div className="tile-hd">
              <Icon name="route" size={12} />
              <span className="ti">Call flow</span>
              <span className="meta">
                {detail ? `${steps.length}${detail.steps.length > STEP_LIMIT ? ` of ${detail.steps.length}` : ''} steps` : 'loading…'}
              </span>
            </div>
            <div className="tile-bd">
              {steps.length === 0 && !procLoading && (
                <div className="faint" style={{ fontSize: 12, padding: 14 }}>
                  No steps available for this process.
                </div>
              )}
              {steps.map((sid, i) => {
                const cur = parseStepId(sid)
                const prev = i > 0 ? parseStepId(steps[i - 1]) : null
                const crosses = prev && prev.repo !== cur.repo ? (
                  <div className="repo-hop">
                    <Icon name="arrowr" size={10} /> crosses {prev.repo || '—'} → {cur.repo || '—'}
                  </div>
                ) : null
                return (
                  <div key={sid + ':' + i}>
                    {crosses}
                    <div
                      className="flow-step"
                      style={{
                        background: stepIdx === i ? 'var(--accent-soft)' : 'transparent',
                        borderRadius: 4,
                        cursor: 'pointer',
                      }}
                      onClick={() => setStepIdx(i)}
                    >
                      <div className="idx">
                        <span className="no">{i + 1}</span>
                      </div>
                      <div className="body">
                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                          {cur.repo && <span className="repo-tag">{cur.repo}</span>}
                          <span className="where">
                            {cur.path ? `${cur.path}:${cur.symbol}` : cur.symbol}
                          </span>
                        </div>
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>

          <div className="inv-tile inv-c-4">
            <div className="tile-hd">
              <Icon name="file" size={12} />
              <span className="ti">Source · step {stepIdx + 1}</span>
              <span className="meta mono">{selectedStepId ? parseStepId(selectedStepId).symbol : ''}</span>
            </div>
            <div className="tile-bd">
              {sourceLoading && (
                <div className="faint" style={{ fontSize: 12 }}>Loading source…</div>
              )}
              {!sourceLoading && !source && (
                <div className="faint" style={{ fontSize: 12 }}>Select a step to view its source.</div>
              )}
              {!sourceLoading && source && (
                <pre className="code" style={{ margin: 0, maxHeight: 420, overflow: 'auto' }}>{source}</pre>
              )}
            </div>
          </div>

          <div className="inv-tile inv-c-6">
            <div className="tile-hd">
              <Icon name="history" size={12} />
              <span className="ti">Recent edits</span>
              <span className="meta">{activity?.length ?? '…'} from /v1/activity</span>
            </div>
            <div className="tile-bd">
              {(activity ?? []).length === 0 && (
                <div className="faint" style={{ fontSize: 12, padding: 14 }}>
                  No recent activity — watch mode may be off, or the server just started.
                </div>
              )}
              {(activity ?? []).slice(0, 10).map((a, i) => (
                <div
                  key={i}
                  style={{
                    display: 'grid',
                    gridTemplateColumns: '70px 16px 1fr',
                    alignItems: 'start',
                    gap: 8,
                    padding: '7px 0',
                    borderBottom: '1px dashed var(--line-1)',
                    fontSize: 12,
                  }}
                >
                  <span className="mono faint" style={{ fontSize: 11 }}>{formatTimeAgo(a.timestamp)}</span>
                  <span style={{ color: 'var(--fg-2)', marginTop: 2 }}>
                    <Icon name={a.kind === 'deleted' ? 'warn' : a.kind === 'created' ? 'check' : 'dot'} size={12} />
                  </span>
                  <span>
                    <span className="mono" style={{ color: 'var(--fg-2)', marginRight: 6 }}>{a.kind}</span>
                    <span className="mono">{a.file_path}</span>
                    <span className="faint mono" style={{ marginLeft: 6 }}>
                      +{a.nodes_added}/-{a.nodes_removed} n
                    </span>
                  </span>
                </div>
              ))}
            </div>
          </div>

          <div className="inv-tile inv-c-6">
            <div className="tile-hd">
              <Icon name="plug" size={12} />
              <span className="ti">Contracts</span>
              <span className="meta">{contracts?.length ?? '…'} from /v1/contracts</span>
            </div>
            <div className="tile-bd" style={{ padding: 0 }}>
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Contract</th>
                    <th>Type</th>
                    <th>Consumers</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {(contracts ?? []).slice(0, 5).map((c) => (
                    <tr key={c.id}>
                      <td className="mono-cell">{c.name}</td>
                      <td>
                        <span className="tag-dim">{c.kind}</span>
                      </td>
                      <td>
                        <div className="hstack" style={{ gap: 4, flexWrap: 'wrap' }}>
                          {c.consumers.map((r, i) => (
                            <span key={i} className="tag-dim">{r}</span>
                          ))}
                        </div>
                      </td>
                      <td>
                        {c.breaking ? (
                          <CaveatBadge kind="boundary" />
                        ) : (
                          <span className="chip" style={{ color: 'var(--ok)' }}>{c.version || 'ok'}</span>
                        )}
                      </td>
                    </tr>
                  ))}
                  {!contracts?.length && (
                    <tr>
                      <td colSpan={4} className="faint" style={{ padding: 14 }}>No contracts indexed.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>

          <div className="inv-tile inv-c-12">
            <div className="tile-hd">
              <Icon name="beaker" size={12} />
              <span className="ti">Guards</span>
              <span className="meta">{guards?.length ?? '…'} from /v1/guards</span>
            </div>
            <div className="tile-bd" style={{ padding: 0 }}>
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Rule</th>
                    <th>Kind</th>
                    <th>Scope</th>
                    <th className="num">Hits</th>
                  </tr>
                </thead>
                <tbody>
                  {(guards ?? []).map((g) => (
                    <tr key={g.id}>
                      <td className="mono-cell">{g.name}</td>
                      <td>
                        <span className="tag-dim">{g.kind}</span>
                      </td>
                      <td className="mono-cell faint">{g.scope}</td>
                      <td className="num">
                        {g.status === 'violated' && <span className="cav risk">{g.hits}</span>}
                        {g.status === 'warn' && <span className="cav deprecated">{g.hits}</span>}
                        {g.status === 'ok' && <span className="faint">0</span>}
                      </td>
                    </tr>
                  ))}
                  {!guards?.length && (
                    <tr>
                      <td colSpan={4} className="faint" style={{ padding: 14 }}>No guards configured.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </div>
    </>
  )
}

function formatTimeAgo(ts: string): string {
  const t = new Date(ts).getTime()
  if (!t) return ts
  const diff = (Date.now() - t) / 1000
  if (diff < 60) return `${Math.floor(diff)}s`
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return `${Math.floor(diff / 86400)}d`
}

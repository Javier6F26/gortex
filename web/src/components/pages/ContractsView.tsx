'use client'

import { useMemo, useState } from 'react'
import { Icon } from '@/components/primitives/Icon'
import { CaveatBadge } from '@/components/primitives/Caveat'
import { CodeBlock } from '@/components/primitives/CodeBlock'
import { useContracts, useSymbolSource, useSymbol, useContractValidation } from '@/lib/hooks'
import type {
  Contract,
  ContractIssue,
  ContractLocation,
  ContractScope,
  ContractType,
  TypeShape,
} from '@/lib/schema'

type ScopeFilter = ContractScope | 'all'
type TypeFilter = ContractType | 'all'

const TYPE_FILTERS: { value: TypeFilter; label: string }[] = [
  { value: 'all', label: 'All types' },
  { value: 'http', label: 'HTTP' },
  { value: 'grpc', label: 'gRPC' },
  { value: 'graphql', label: 'GraphQL' },
  { value: 'topic', label: 'Topic' },
  { value: 'ws', label: 'WS' },
  { value: 'env', label: 'Env' },
  { value: 'openapi', label: 'OpenAPI' },
  { value: 'dependency', label: 'Dep' },
]

const SCOPE_FILTERS: { value: ScopeFilter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'own', label: 'Own' },
  { value: 'external', label: 'External' },
]

export function ContractsView() {
  const { data, loading, error, refetch } = useContracts()
  const contracts = data ?? []
  const { data: validation, refetch: refetchValidation } = useContractValidation()

  const [scope, setScope] = useState<ScopeFilter>('all')
  const [typ, setTyp] = useState<TypeFilter>('all')
  const [openId, setOpenId] = useState<string | null>(null)

  const typeCounts = useMemo(() => countBy(contracts, (c) => c.type), [contracts])
  const scopeCounts = useMemo(() => countBy(contracts, (c) => c.scope), [contracts])

  // Bucket validation issues by contract ID so every row can look up
  // its own diffs in O(1). Also compute severity-summary for the row
  // so the badge renders without re-scanning the full list.
  const issuesByContract = useMemo(() => {
    const m = new Map<string, ContractIssue[]>()
    for (const is of validation?.issues ?? []) {
      const bucket = m.get(is.contract_id) ?? []
      bucket.push(is)
      m.set(is.contract_id, bucket)
    }
    return m
  }, [validation])

  const filtered = useMemo(
    () =>
      contracts.filter(
        (c) => (scope === 'all' || c.scope === scope) && (typ === 'all' || c.type === typ),
      ),
    [contracts, scope, typ],
  )
  // Breaking count is derived from validation (the contract-level
  // `breaking` flag is still unused / false pending future work).
  const breakingTotal = validation?.summary.breaking ?? 0
  const warningTotal = validation?.summary.warning ?? 0

  const refresh = () => {
    refetch()
    refetchValidation()
  }

  return (
    <>
      <div className="page-hd">
        <div>
          <h1>Contracts</h1>
          <div className="sub">
            {loading
              ? 'Loading detected contracts…'
              : validation
              ? `${filtered.length} of ${contracts.length} API/event boundaries · ${breakingTotal} breaking · ${warningTotal} warning`
              : `${filtered.length} of ${contracts.length} API/event boundaries`}
          </div>
        </div>
        <div className="actions">
          <button type="button" className="btn" onClick={refresh}>
            <Icon name="history" size={12} /> Refresh
          </button>
        </div>
      </div>

      <div
        style={{
          display: 'flex',
          gap: 16,
          padding: '12px 22px 0',
          flexWrap: 'wrap',
          alignItems: 'center',
        }}
      >
        <div className="hstack" style={{ gap: 0, border: '1px solid var(--line)', borderRadius: 6, overflow: 'hidden' }}>
          {SCOPE_FILTERS.map((s) => {
            const active = scope === s.value
            const count = s.value === 'all' ? contracts.length : scopeCounts.get(s.value) ?? 0
            return (
              <button
                key={s.value}
                type="button"
                onClick={() => setScope(s.value)}
                style={{
                  padding: '6px 12px',
                  fontSize: 12,
                  border: 'none',
                  borderRight: '1px solid var(--line)',
                  background: active ? 'var(--bg-1)' : 'transparent',
                  color: active ? 'var(--fg-0)' : 'var(--fg-2)',
                  cursor: 'pointer',
                  fontWeight: active ? 600 : 400,
                }}
              >
                {s.label}
                <span className="faint" style={{ marginLeft: 6 }}>{count}</span>
              </button>
            )
          })}
        </div>

        <div className="hstack" style={{ gap: 6, flexWrap: 'wrap' }}>
          {TYPE_FILTERS.map((t) => {
            const count = t.value === 'all' ? contracts.length : typeCounts.get(t.value) ?? 0
            if (t.value !== 'all' && count === 0) return null
            const active = typ === t.value
            return (
              <button
                key={t.value}
                type="button"
                onClick={() => setTyp(t.value)}
                className="chip"
                style={{
                  cursor: 'pointer',
                  background: active ? 'var(--bg-1)' : 'transparent',
                  color: active ? 'var(--fg-0)' : 'var(--fg-2)',
                  borderColor: active ? 'var(--fg-2)' : 'var(--line)',
                  fontWeight: active ? 600 : 400,
                }}
              >
                {t.label} <span className="faint" style={{ marginLeft: 4 }}>{count}</span>
              </button>
            )
          })}
        </div>
      </div>

      {error && (
        <div style={{ padding: 22, color: 'var(--danger)', fontSize: 13 }}>
          Failed to load contracts: {error}
        </div>
      )}

      {!error && contracts.length === 0 && !loading && (
        <div style={{ padding: 22, color: 'var(--fg-2)', fontSize: 13 }}>
          No contracts detected. Make sure the indexer ran on a repository that exposes HTTP, gRPC, or event topics.
        </div>
      )}

      {!error && contracts.length > 0 && filtered.length === 0 && (
        <div style={{ padding: 22, color: 'var(--fg-2)', fontSize: 13 }}>
          No contracts match the current filters.
        </div>
      )}

      {filtered.length > 0 && (
        <div style={{ padding: '18px 22px', overflow: 'auto' }}>
          <div style={{ display: 'grid', gap: 10 }}>
            {filtered.map((c) => (
              <ContractRow
                key={c.id}
                c={c}
                issues={issuesByContract.get(c.id) ?? []}
                expanded={openId === c.id}
                onToggle={() => setOpenId(openId === c.id ? null : c.id)}
              />
            ))}
          </div>
        </div>
      )}
    </>
  )
}

function ContractRow({
  c,
  issues,
  expanded,
  onToggle,
}: {
  c: Contract
  issues: ContractIssue[]
  expanded: boolean
  onToggle: () => void
}) {
  const badge = kindBadge(c.kind)
  const [selected, setSelected] = useState<ContractLocation | null>(null)
  const [mode, setMode] = useState<'source' | 'schema' | 'issues'>('source')
  const providerLoc = c.locations.find((l) => l.role === 'provider') ?? null

  const breakingCount = issues.filter((i) => i.severity === 'breaking').length
  const warningCount = issues.filter((i) => i.severity === 'warning').length
  const infoCount = issues.filter((i) => i.severity === 'info').length

  const openTrace = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!expanded) onToggle()
    setMode('source')
    setSelected(providerLoc ?? c.locations[0] ?? null)
  }
  const openSchema = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (!expanded) onToggle()
    setMode('schema')
    if (!selected) setSelected(providerLoc ?? c.locations[0] ?? null)
  }

  return (
    <div className="card">
      <div
        onClick={onToggle}
        style={{
          display: 'grid',
          gridTemplateColumns: '28px 1fr auto',
          gap: 14,
          padding: 14,
          alignItems: 'center',
          cursor: 'pointer',
        }}
      >
        <div
          style={{
            width: 28,
            height: 28,
            borderRadius: 6,
            background: badge.bg,
            color: badge.fg,
            display: 'grid',
            placeItems: 'center',
            fontFamily: 'JetBrains Mono',
            fontSize: 10,
            fontWeight: 600,
          }}
        >
          {badge.label}
        </div>
        <div>
          <div className="hstack" style={{ gap: 8, flexWrap: 'wrap' }}>
            <Icon name={expanded ? 'caretdn' : 'caret'} size={10} />
            <span className="mono" style={{ fontSize: 14, color: 'var(--fg-0)' }}>{c.name}</span>
            <span className="chip" title={`type: ${c.type}`}>{c.type}</span>
            <span
              className="chip"
              title={c.scope === 'own' ? 'Defined in this project' : 'External or consumed-only'}
              style={{
                color: c.scope === 'own' ? 'var(--fg-0)' : 'var(--fg-2)',
                borderColor: c.scope === 'own' ? 'var(--fg-2)' : 'var(--line)',
              }}
            >
              {c.scope}
            </span>
            {c.breaking && <CaveatBadge kind="boundary" />}
            {c.version && <span className="chip">{c.version}</span>}
            {breakingCount > 0 && (
              <span
                className="chip"
                title={`${breakingCount} breaking change${breakingCount === 1 ? '' : 's'} — click to inspect`}
                style={{
                  color: 'var(--danger)',
                  borderColor: 'var(--danger)',
                  background: 'oklch(0.6 0.22 25 / 0.1)',
                  fontWeight: 600,
                }}
              >
                ⚠ {breakingCount} breaking
              </span>
            )}
            {warningCount > 0 && (
              <span
                className="chip"
                title={`${warningCount} warning${warningCount === 1 ? '' : 's'}`}
                style={{ color: 'var(--warn)', borderColor: 'var(--warn)' }}
              >
                {warningCount} warning
              </span>
            )}
          </div>
          <div className="hstack" style={{ gap: 10, marginTop: 6, fontSize: 11.5, color: 'var(--fg-2)', flexWrap: 'wrap' }}>
            <span>
              Produced by <span className="tag-dim">{c.producer || 'unknown'}</span>
            </span>
            {c.consumers.length > 0 && (
              <>
                <span>→</span>
                <span className="hstack" style={{ gap: 4 }}>
                  consumed by{' '}
                  {c.consumers.map((r) => (
                    <span key={r} className="tag-dim">{r}</span>
                  ))}
                </span>
              </>
            )}
            <span className="faint">· {c.locations.length} location{c.locations.length === 1 ? '' : 's'}</span>
          </div>
        </div>
        <div className="hstack" style={{ gap: 6 }}>
          <button type="button" className="btn small ghost" onClick={openTrace}>
            <Icon name="graph" size={11} /> Trace
          </button>
          <button type="button" className="btn small" onClick={openSchema}>
            <Icon name="file" size={11} /> Schema
          </button>
        </div>
      </div>

      {expanded && (
        <ContractDetail
          c={c}
          issues={issues}
          selected={selected}
          onSelect={setSelected}
          mode={mode}
          setMode={setMode}
        />
      )}
    </div>
  )
}

function ContractDetail({
  c,
  issues,
  selected,
  onSelect,
  mode,
  setMode,
}: {
  c: Contract
  issues: ContractIssue[]
  selected: ContractLocation | null
  onSelect: (l: ContractLocation) => void
  mode: 'source' | 'schema' | 'issues'
  setMode: (m: 'source' | 'schema' | 'issues') => void
}) {
  const providers = c.locations.filter((l) => l.role === 'provider')
  const consumers = c.locations.filter((l) => l.role === 'consumer')

  return (
    <div
      style={{
        borderTop: '1px solid var(--line)',
        display: 'grid',
        gridTemplateColumns: 'minmax(240px, 320px) 1fr',
        minHeight: 220,
      }}
    >
      <div
        style={{
          borderRight: '1px solid var(--line)',
          padding: '10px 12px',
          maxHeight: 480,
          overflow: 'auto',
          fontSize: 12,
        }}
      >
        {providers.length > 0 && (
          <LocationGroup
            label="Providers"
            locations={providers}
            selected={selected}
            onSelect={onSelect}
          />
        )}
        {consumers.length > 0 && (
          <LocationGroup
            label="Consumers"
            locations={consumers}
            selected={selected}
            onSelect={onSelect}
          />
        )}
        {c.locations.length === 0 && (
          <div className="faint" style={{ padding: 8 }}>No locations recorded.</div>
        )}
      </div>

      <div style={{ padding: '10px 12px', display: 'grid', gridTemplateRows: 'auto 1fr', minHeight: 0 }}>
        <div className="hstack" style={{ gap: 6, marginBottom: 8 }}>
          <button
            type="button"
            className="chip"
            onClick={() => setMode('source')}
            style={{
              cursor: 'pointer',
              background: mode === 'source' ? 'var(--bg-1)' : 'transparent',
              color: mode === 'source' ? 'var(--fg-0)' : 'var(--fg-2)',
              borderColor: mode === 'source' ? 'var(--fg-2)' : 'var(--line)',
              fontWeight: mode === 'source' ? 600 : 400,
            }}
          >
            Source
          </button>
          <button
            type="button"
            className="chip"
            onClick={() => setMode('schema')}
            style={{
              cursor: 'pointer',
              background: mode === 'schema' ? 'var(--bg-1)' : 'transparent',
              color: mode === 'schema' ? 'var(--fg-0)' : 'var(--fg-2)',
              borderColor: mode === 'schema' ? 'var(--fg-2)' : 'var(--line)',
              fontWeight: mode === 'schema' ? 600 : 400,
            }}
          >
            Schema / Meta
          </button>
          {issues.length > 0 && (
            <button
              type="button"
              className="chip"
              onClick={() => setMode('issues')}
              style={{
                cursor: 'pointer',
                background: mode === 'issues' ? 'var(--bg-1)' : 'transparent',
                color:
                  mode === 'issues'
                    ? 'var(--fg-0)'
                    : issues.some((i) => i.severity === 'breaking')
                    ? 'var(--danger)'
                    : 'var(--fg-2)',
                borderColor:
                  mode === 'issues'
                    ? 'var(--fg-2)'
                    : issues.some((i) => i.severity === 'breaking')
                    ? 'var(--danger)'
                    : 'var(--line)',
                fontWeight: mode === 'issues' ? 600 : 400,
              }}
            >
              Issues · {issues.length}
            </button>
          )}
          {selected && (
            <span className="faint mono" style={{ marginLeft: 'auto', fontSize: 11 }}>
              {selected.file_path}:{selected.line}
            </span>
          )}
        </div>

        {mode === 'source' ? (
          <SourcePane symbolId={selected?.symbol_id ?? null} filePath={selected?.file_path ?? null} />
        ) : mode === 'schema' ? (
          <SchemaPane contract={c} loc={selected} />
        ) : (
          <IssuesPane issues={issues} />
        )}
      </div>
    </div>
  )
}

function IssuesPane({ issues }: { issues: ContractIssue[] }) {
  if (issues.length === 0) {
    return (
      <div className="faint" style={{ padding: 12, fontSize: 12 }}>
        No validation issues detected for this contract. Provider and
        consumer shapes match.
      </div>
    )
  }
  return (
    <div style={{ display: 'grid', gap: 6, overflow: 'auto' }}>
      {issues.map((is, i) => (
        <IssueRow key={`${is.kind}-${is.field}-${i}`} issue={is} />
      ))}
    </div>
  )
}

function IssueRow({ issue }: { issue: ContractIssue }) {
  const sevColor =
    issue.severity === 'breaking'
      ? 'var(--danger)'
      : issue.severity === 'warning'
      ? 'var(--warn)'
      : 'var(--fg-2)'
  const sevBg =
    issue.severity === 'breaking'
      ? 'oklch(0.6 0.22 25 / 0.08)'
      : issue.severity === 'warning'
      ? 'oklch(0.82 0.15 80 / 0.08)'
      : 'transparent'
  return (
    <div
      style={{
        border: '1px solid var(--line)',
        borderLeft: `3px solid ${sevColor}`,
        background: sevBg,
        borderRadius: 4,
        padding: '8px 10px',
        fontSize: 12,
        display: 'grid',
        gap: 4,
      }}
    >
      <div className="hstack" style={{ gap: 8, flexWrap: 'wrap' }}>
        <span
          className="chip"
          style={{
            color: sevColor,
            borderColor: sevColor,
            fontWeight: 600,
            fontSize: 10,
            textTransform: 'uppercase',
          }}
        >
          {issue.severity}
        </span>
        <span className="mono" style={{ fontSize: 11.5 }}>{issue.kind}</span>
        {issue.field && (
          <span className="mono faint" style={{ fontSize: 11 }}>field={issue.field}</span>
        )}
      </div>
      {issue.details && (
        <div className="faint" style={{ fontSize: 11.5, lineHeight: 1.45 }}>{issue.details}</div>
      )}
      <div className="hstack" style={{ gap: 6, flexWrap: 'wrap', fontSize: 10.5, color: 'var(--fg-2)' }}>
        {issue.provider && <span>provider={issue.provider}</span>}
        {issue.consumer && <span>consumer={issue.consumer}</span>}
        {issue.provider_type && (
          <span className="mono" title="Provider type">p={issue.provider_type}</span>
        )}
        {issue.consumer_type && (
          <span className="mono" title="Consumer type">c={issue.consumer_type}</span>
        )}
      </div>
    </div>
  )
}

function LocationGroup({
  label,
  locations,
  selected,
  onSelect,
}: {
  label: string
  locations: ContractLocation[]
  selected: ContractLocation | null
  onSelect: (l: ContractLocation) => void
}) {
  const byRepo = new Map<string, ContractLocation[]>()
  for (const l of locations) {
    const key = l.repo_prefix || '(unknown)'
    const bucket = byRepo.get(key) ?? []
    bucket.push(l)
    byRepo.set(key, bucket)
  }
  return (
    <div style={{ marginBottom: 12 }}>
      <div className="faint" style={{ textTransform: 'uppercase', fontSize: 10, letterSpacing: 0.5, marginBottom: 6 }}>
        {label} · {locations.length}
      </div>
      {[...byRepo.entries()].map(([repo, items]) => (
        <div key={repo} style={{ marginBottom: 8 }}>
          <div className="tag-dim" style={{ marginBottom: 4 }}>{repo}</div>
          <div style={{ display: 'grid', gap: 2 }}>
            {items.map((l, i) => {
              const isSel = selected === l
              return (
                <button
                  key={`${l.file_path}:${l.line}:${i}`}
                  type="button"
                  onClick={() => onSelect(l)}
                  className="mono"
                  title={l.symbol_id}
                  style={{
                    textAlign: 'left',
                    background: isSel ? 'var(--bg-1)' : 'transparent',
                    color: isSel ? 'var(--fg-0)' : 'var(--fg-2)',
                    border: '1px solid',
                    borderColor: isSel ? 'var(--fg-2)' : 'transparent',
                    borderRadius: 4,
                    padding: '3px 6px',
                    fontSize: 11,
                    cursor: 'pointer',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {l.file_path}:{l.line}
                </button>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}

function SourcePane({ symbolId, filePath }: { symbolId: string | null; filePath: string | null }) {
  const { data, loading, error } = useSymbolSource(symbolId)
  if (!symbolId) {
    return (
      <div className="faint" style={{ padding: 12, fontSize: 12 }}>
        Select a location on the left to view its source.
      </div>
    )
  }
  if (loading) return <div className="faint" style={{ padding: 12 }}>Loading source…</div>
  if (error) return <div style={{ padding: 12, color: 'var(--danger)', fontSize: 12 }}>Failed to load source: {error}</div>
  if (!data) return <div className="faint" style={{ padding: 12, fontSize: 12 }}>No source available for {symbolId}.</div>
  return <CodeBlock code={data} filePath={filePath ?? undefined} maxHeight={420} />
}

function SchemaPane({ contract, loc }: { contract: Contract; loc: ContractLocation | null }) {
  const schema = contract.schema
  const hasSchema =
    !!schema &&
    (!!schema.request_type ||
      !!schema.response_type ||
      !!schema.request_expr ||
      !!schema.response_expr ||
      (schema.path_params?.length ?? 0) > 0 ||
      (schema.query_params?.length ?? 0) > 0 ||
      (schema.status_codes?.length ?? 0) > 0)

  return (
    <div style={{ display: 'grid', gap: 10, overflow: 'auto' }}>
      {hasSchema && schema ? (
        <div style={{ display: 'grid', gap: 10 }}>
          <div className="hstack" style={{ gap: 6, flexWrap: 'wrap', fontSize: 11.5 }}>
            <span className="chip" title="Contract type">{contract.type}</span>
            {schema.source && (
              <span
                className="chip"
                title="How the schema was inferred"
                style={{
                  color:
                    schema.source === 'extracted'
                      ? 'var(--ok)'
                      : schema.source === 'partial'
                      ? 'var(--warn)'
                      : 'var(--fg-2)',
                }}
              >
                {schema.source}
              </span>
            )}
          </div>

          <SchemaField label="Request" type={schema.request_type} expr={schema.request_expr} stream={schema.request_stream} />
          <TypeShapeInline symbolId={schemaTypeSymbolId(schema.request_type)} />
          <SchemaField label="Response" type={schema.response_type} expr={schema.response_expr} stream={schema.response_stream} />
          <TypeShapeInline symbolId={schemaTypeSymbolId(schema.response_type)} />

          {(schema.path_params?.length ?? 0) > 0 && (
            <ParamRow label="Path params" values={schema.path_params!} />
          )}
          {(schema.query_params?.length ?? 0) > 0 && (
            <ParamRow label="Query params" values={schema.query_params!} />
          )}
          {(schema.status_codes?.length ?? 0) > 0 && (
            <ParamRow label="Status codes" values={schema.status_codes!.map(String)} />
          )}
        </div>
      ) : (
        <div className="faint" style={{ padding: 12, fontSize: 12 }}>
          No schema shape was extracted for this contract. The extractor
          either didn&apos;t recognise the framework binding, or the
          handler writes an inline / anonymous type. Raw per-location
          meta is shown below.
        </div>
      )}

      {loc?.meta && Object.keys(loc.meta).length > 0 && (
        <div style={{ display: 'grid', gap: 6 }}>
          <div className="faint" style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: 0.5 }}>
            Location meta {loc.symbol_id ? `· ${loc.symbol_id}` : ''}
          </div>
          <CodeBlock code={JSON.stringify(loc.meta, null, 2)} lang="json" maxHeight={240} />
        </div>
      )}
    </div>
  )
}

function SchemaField({
  label,
  type,
  expr,
  stream,
}: {
  label: string
  type?: string
  expr?: string
  stream?: boolean
}) {
  if (!type && !expr) return null
  const isSymbolId = !!type && type.includes('::')
  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: '90px 1fr',
        gap: 10,
        alignItems: 'baseline',
        fontSize: 12,
      }}
    >
      <div className="faint" style={{ textTransform: 'uppercase', fontSize: 10, letterSpacing: 0.5 }}>
        {label}
        {stream && <span style={{ marginLeft: 4, color: 'var(--violet)' }}>stream</span>}
      </div>
      <div className="hstack" style={{ gap: 6, flexWrap: 'wrap' }}>
        {type ? (
          <span
            className="mono"
            title={isSymbolId ? 'Symbol ID (resolved)' : 'Bare type name (unresolved across repos)'}
            style={{
              color: isSymbolId ? 'var(--fg-0)' : 'var(--fg-2)',
              background: 'var(--bg-1)',
              border: '1px solid var(--line)',
              borderRadius: 4,
              padding: '2px 6px',
              fontSize: 11.5,
            }}
          >
            {type}
          </span>
        ) : (
          <span className="mono faint" style={{ fontSize: 11 }}>{expr}</span>
        )}
      </div>
    </div>
  )
}

function ParamRow({ label, values }: { label: string; values: string[] }) {
  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: '90px 1fr',
        gap: 10,
        alignItems: 'baseline',
        fontSize: 12,
      }}
    >
      <div className="faint" style={{ textTransform: 'uppercase', fontSize: 10, letterSpacing: 0.5 }}>
        {label}
      </div>
      <div className="hstack" style={{ gap: 4, flexWrap: 'wrap' }}>
        {values.map((v) => (
          <span
            key={v}
            className="mono"
            style={{
              background: 'var(--bg-1)',
              border: '1px solid var(--line)',
              borderRadius: 4,
              padding: '2px 6px',
              fontSize: 11,
            }}
          >
            {v}
          </span>
        ))}
      </div>
    </div>
  )
}

// schemaTypeSymbolId returns the type identifier only when it's a
// graph symbol ID (contains `::`), otherwise null. Bare names that
// the module-wide post-pass couldn't upgrade don't resolve to nodes,
// so we can't fetch shapes for them.
function schemaTypeSymbolId(t?: string): string | null {
  if (!t) return null
  return t.includes('::') ? t : null
}

// TypeShapeInline fetches the type node for a symbol ID and renders
// its field-level shape (Stage 2 output). No-op when the symbolId is
// null, when the node has no shape attached, or while the fetch is
// in flight — the surrounding Request/Response row already conveys
// the type name, so this is purely additive.
function TypeShapeInline({ symbolId }: { symbolId: string | null }) {
  const { data: node } = useSymbol(symbolId)
  const shape = (node?.meta?.shape ?? null) as TypeShape | null
  if (!shape || shape.fields.length === 0) return null

  return (
    <div
      style={{
        marginLeft: 100,
        marginTop: -4,
        border: '1px solid var(--line)',
        borderRadius: 4,
        overflow: 'hidden',
        fontSize: 11.5,
      }}
    >
      <div
        className="faint"
        style={{
          padding: '4px 8px',
          background: 'var(--bg-1)',
          borderBottom: '1px solid var(--line)',
          textTransform: 'uppercase',
          fontSize: 10,
          letterSpacing: 0.5,
        }}
      >
        {shape.kind} · {shape.fields.length} field{shape.fields.length === 1 ? '' : 's'}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'max-content max-content 1fr' }}>
        {shape.fields.map((f) => (
          <TypeShapeRow key={f.name} f={f} />
        ))}
      </div>
    </div>
  )
}

function TypeShapeRow({ f }: { f: { name: string; type: string; required: boolean; repeated?: boolean; json_tag?: string; comment?: string } }) {
  return (
    <>
      <div
        className="mono"
        style={{
          padding: '3px 8px',
          color: f.required ? 'var(--fg-0)' : 'var(--fg-2)',
        }}
      >
        {f.name}
        {f.required ? '' : <span className="faint">?</span>}
      </div>
      <div className="mono faint" style={{ padding: '3px 8px' }}>
        {f.repeated ? `${f.type}[]` : f.type}
      </div>
      <div className="faint" style={{ padding: '3px 8px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {f.json_tag && f.json_tag !== f.name && <span className="mono" style={{ marginRight: 8 }}>tag={f.json_tag}</span>}
        {f.comment}
      </div>
    </>
  )
}

function countBy<T, K>(xs: T[], key: (x: T) => K): Map<K, number> {
  const m = new Map<K, number>()
  for (const x of xs) m.set(key(x), (m.get(key(x)) ?? 0) + 1)
  return m
}

function kindBadge(kind: string): { label: string; bg: string; fg: string } {
  switch (kind) {
    case 'EVENT':
      return { label: 'EV', bg: 'oklch(0.78 0.14 300 / 0.18)', fg: 'var(--violet)' }
    case 'URL':
      return { label: 'URL', bg: 'oklch(0.82 0.15 80 / 0.18)', fg: 'var(--warn)' }
    case 'ENV':
      return { label: 'ENV', bg: 'oklch(0.8 0.1 140 / 0.18)', fg: 'var(--ok)' }
    case 'DEP':
      return { label: 'DEP', bg: 'oklch(0.7 0.08 260 / 0.18)', fg: 'var(--fg-2)' }
    default:
      return { label: 'API', bg: 'oklch(0.82 0.14 45 / 0.18)', fg: 'var(--k-contract)' }
  }
}

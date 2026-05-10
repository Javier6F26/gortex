package lsp

// LSP protocol types — minimal subset needed for semantic enrichment.
// Based on LSP 3.17.

// InitializeParams is sent as the first request to the server.
type InitializeParams struct {
	ProcessID    int                `json:"processId"`
	RootURI      string             `json:"rootUri"`
	Capabilities ClientCapabilities `json:"capabilities"`
}

// ClientCapabilities declares what the client supports.
type ClientCapabilities struct {
	Workspace    *WorkspaceClientCapabilities   `json:"workspace,omitempty"`
	TextDocument TextDocumentClientCapabilities `json:"textDocument,omitempty"`
}

// WorkspaceClientCapabilities declares workspace-level capabilities.
type WorkspaceClientCapabilities struct {
	ApplyEdit        bool                             `json:"applyEdit,omitempty"`
	WorkspaceEdit    *WorkspaceEditClientCapabilities `json:"workspaceEdit,omitempty"`
	ExecuteCommand   *ExecuteCommandCapability        `json:"executeCommand,omitempty"`
	WorkspaceFolders bool                             `json:"workspaceFolders,omitempty"`
	Configuration    bool                             `json:"configuration,omitempty"`
}

// WorkspaceEditClientCapabilities declares applyEdit support.
type WorkspaceEditClientCapabilities struct {
	DocumentChanges    bool     `json:"documentChanges,omitempty"`
	ResourceOperations []string `json:"resourceOperations,omitempty"`
}

// ExecuteCommandCapability declares workspace/executeCommand support.
type ExecuteCommandCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// TextDocumentClientCapabilities declares text document capabilities.
type TextDocumentClientCapabilities struct {
	Implementation     *ImplementationCapability     `json:"implementation,omitempty"`
	References         *ReferencesCapability         `json:"references,omitempty"`
	Definition         *DefinitionCapability         `json:"definition,omitempty"`
	Hover              *HoverCapability              `json:"hover,omitempty"`
	CallHierarchy      *CallHierarchyCapability      `json:"callHierarchy,omitempty"`
	TypeHierarchy      *TypeHierarchyCapability      `json:"typeHierarchy,omitempty"`
	CodeAction         *CodeActionCapability         `json:"codeAction,omitempty"`
	PublishDiagnostics *PublishDiagnosticsCapability `json:"publishDiagnostics,omitempty"`
	Synchronization    *SynchronizationCapability    `json:"synchronization,omitempty"`
}

// SynchronizationCapability advertises didOpen/didChange support.
type SynchronizationCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
	WillSave            bool `json:"willSave,omitempty"`
	DidSave             bool `json:"didSave,omitempty"`
}

// CodeActionCapability advertises codeAction request support.
type CodeActionCapability struct {
	DynamicRegistration      bool                      `json:"dynamicRegistration,omitempty"`
	CodeActionLiteralSupport *CodeActionLiteralSupport `json:"codeActionLiteralSupport,omitempty"`
	IsPreferredSupport       bool                      `json:"isPreferredSupport,omitempty"`
	DataSupport              bool                      `json:"dataSupport,omitempty"`
	ResolveSupport           *CodeActionResolveSupport `json:"resolveSupport,omitempty"`
}

// CodeActionLiteralSupport declares supported code action kinds.
type CodeActionLiteralSupport struct {
	CodeActionKind CodeActionKindCapability `json:"codeActionKind"`
}

// CodeActionKindCapability lists known code action kinds.
type CodeActionKindCapability struct {
	ValueSet []string `json:"valueSet"`
}

// CodeActionResolveSupport declares which CodeAction fields the
// server can be asked to resolve lazily via codeAction/resolve.
type CodeActionResolveSupport struct {
	Properties []string `json:"properties"`
}

// PublishDiagnosticsCapability advertises diagnostic-related features.
type PublishDiagnosticsCapability struct {
	RelatedInformation     bool `json:"relatedInformation,omitempty"`
	VersionSupport         bool `json:"versionSupport,omitempty"`
	CodeDescriptionSupport bool `json:"codeDescriptionSupport,omitempty"`
	DataSupport            bool `json:"dataSupport,omitempty"`
}

// CallHierarchyCapability declares callHierarchy/* request support.
type CallHierarchyCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// TypeHierarchyCapability declares typeHierarchy/* request support.
type TypeHierarchyCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// ImplementationCapability declares implementation request support.
type ImplementationCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// ReferencesCapability declares references request support.
type ReferencesCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// DefinitionCapability declares definition request support.
type DefinitionCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

// HoverCapability declares hover request support.
type HoverCapability struct {
	DynamicRegistration bool     `json:"dynamicRegistration,omitempty"`
	ContentFormat       []string `json:"contentFormat,omitempty"`
}

// InitializeResult is the server's response to initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ServerCapabilities declares what the server supports.
type ServerCapabilities struct {
	ImplementationProvider any                    `json:"implementationProvider,omitempty"`
	ReferencesProvider     any                    `json:"referencesProvider,omitempty"`
	DefinitionProvider     any                    `json:"definitionProvider,omitempty"`
	HoverProvider          any                    `json:"hoverProvider,omitempty"`
	CallHierarchyProvider  any                    `json:"callHierarchyProvider,omitempty"`
	TypeHierarchyProvider  any                    `json:"typeHierarchyProvider,omitempty"`
	CodeActionProvider     any                    `json:"codeActionProvider,omitempty"`
	ExecuteCommandProvider *ExecuteCommandOptions `json:"executeCommandProvider,omitempty"`
	TextDocumentSync       any                    `json:"textDocumentSync,omitempty"`
}

// ExecuteCommandOptions tells which commands the server can execute.
type ExecuteCommandOptions struct {
	Commands []string `json:"commands,omitempty"`
}

// TextDocumentIdentifier identifies a text document.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// TextDocumentPositionParams identifies a position in a text document.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Position in a text document (0-indexed).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Location represents a location in a document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Range in a text document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// DidOpenTextDocumentParams is sent when a document is opened.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentItem is a text document with content.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// ReferenceParams extends TextDocumentPositionParams with reference context.
type ReferenceParams struct {
	TextDocumentPositionParams
	Context ReferenceContext `json:"context"`
}

// ReferenceContext controls what references are returned.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// ImplementationParams is the params for textDocument/implementation.
type ImplementationParams struct {
	TextDocumentPositionParams
}

// HoverParams is the params for textDocument/hover.
type HoverParams struct {
	TextDocumentPositionParams
}

// HoverResult is the response for textDocument/hover.
type HoverResult struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// MarkupContent represents hover content.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// CallHierarchyPrepareParams is the params for callHierarchy/prepare.
type CallHierarchyPrepareParams struct {
	TextDocumentPositionParams
}

// CallHierarchyItem represents an item in the call hierarchy.
type CallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	URI            string `json:"uri"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}

// CallHierarchyIncomingCallsParams is the params for callHierarchy/incomingCalls.
type CallHierarchyIncomingCallsParams struct {
	Item CallHierarchyItem `json:"item"`
}

// CallHierarchyIncomingCall represents an incoming call.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCallsParams is the params for callHierarchy/outgoingCalls.
type CallHierarchyOutgoingCallsParams struct {
	Item CallHierarchyItem `json:"item"`
}

// CallHierarchyOutgoingCall represents an outgoing call.
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// TypeHierarchyPrepareParams is the params for textDocument/prepareTypeHierarchy.
type TypeHierarchyPrepareParams struct {
	TextDocumentPositionParams
}

// TypeHierarchyItem represents an item in the type hierarchy.
type TypeHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	URI            string `json:"uri"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}

// TypeHierarchySupertypesParams is the params for typeHierarchy/supertypes.
type TypeHierarchySupertypesParams struct {
	Item TypeHierarchyItem `json:"item"`
}

// TypeHierarchySubtypesParams is the params for typeHierarchy/subtypes.
type TypeHierarchySubtypesParams struct {
	Item TypeHierarchyItem `json:"item"`
}

// ---------------------------------------------------------------------------
// textDocument/codeAction support (LSP fix-all).
// ---------------------------------------------------------------------------

// Standard LSP code-action kinds (a subset).
const (
	CodeActionKindEmpty                 = ""
	CodeActionKindQuickFix              = "quickfix"
	CodeActionKindRefactor              = "refactor"
	CodeActionKindRefactorExtract       = "refactor.extract"
	CodeActionKindRefactorInline        = "refactor.inline"
	CodeActionKindRefactorRewrite       = "refactor.rewrite"
	CodeActionKindSource                = "source"
	CodeActionKindSourceOrganizeImports = "source.organizeImports"
	CodeActionKindSourceFixAll          = "source.fixAll"
)

// DiagnosticSeverity values match the LSP spec.
const (
	DiagSeverityError       = 1
	DiagSeverityWarning     = 2
	DiagSeverityInformation = 3
	DiagSeverityHint        = 4
)

// Diagnostic is a problem report from the server.
type Diagnostic struct {
	Range              Range                          `json:"range"`
	Severity           int                            `json:"severity,omitempty"`
	Code               any                            `json:"code,omitempty"`
	CodeDescription    *DiagnosticCodeDescription     `json:"codeDescription,omitempty"`
	Source             string                         `json:"source,omitempty"`
	Message            string                         `json:"message"`
	Tags               []int                          `json:"tags,omitempty"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
	Data               any                            `json:"data,omitempty"`
}

// DiagnosticCodeDescription points to documentation for the code.
type DiagnosticCodeDescription struct {
	Href string `json:"href"`
}

// DiagnosticRelatedInformation locates a related document position.
type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

// PublishDiagnosticsParams is the payload of textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// CodeActionParams is the params for textDocument/codeAction.
type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

// CodeActionContext narrows the kinds of actions returned and lists
// diagnostics those actions should address.
type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	Only        []string     `json:"only,omitempty"`
	TriggerKind int          `json:"triggerKind,omitempty"`
}

// Command is a server-defined command (e.g. "go.organizeImports").
type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeAction is the rich form returned from textDocument/codeAction.
// Either Edit or Command (or both) may be set; servers vary.
type CodeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind,omitempty"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
	IsPreferred bool            `json:"isPreferred,omitempty"`
	Disabled    *DisabledReason `json:"disabled,omitempty"`
	Edit        *WorkspaceEdit  `json:"edit,omitempty"`
	Command     *Command        `json:"command,omitempty"`
	Data        any             `json:"data,omitempty"`
}

// DisabledReason explains why a server marked a code action disabled.
type DisabledReason struct {
	Reason string `json:"reason"`
}

// CodeActionOrCommand is the union returned by textDocument/codeAction.
// LSP servers may emit either a Command (legacy) or a CodeAction
// literal — we accept both and normalise downstream.
type CodeActionOrCommand struct {
	// Title is always present and human readable.
	Title string `json:"title,omitempty"`
	// Command is set on legacy (Command) form.
	Command string `json:"command,omitempty"`
	// Arguments accompanies legacy Command form.
	Arguments []any `json:"arguments,omitempty"`
	// Kind / Edit / Diagnostics / IsPreferred / Disabled are part of
	// the CodeAction literal form.
	Kind        string          `json:"kind,omitempty"`
	Edit        *WorkspaceEdit  `json:"edit,omitempty"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
	IsPreferred bool            `json:"isPreferred,omitempty"`
	Disabled    *DisabledReason `json:"disabled,omitempty"`
	Data        any             `json:"data,omitempty"`
}

// IsCommand reports whether the action is a legacy Command (no edit).
func (a *CodeActionOrCommand) IsCommand() bool {
	return a != nil && a.Command != "" && a.Edit == nil
}

// AsCodeAction projects the union into the literal CodeAction form.
func (a *CodeActionOrCommand) AsCodeAction() *CodeAction {
	if a == nil {
		return nil
	}
	if a.Edit == nil && a.Command == "" {
		return &CodeAction{Title: a.Title, Kind: a.Kind, IsPreferred: a.IsPreferred, Diagnostics: a.Diagnostics, Disabled: a.Disabled, Data: a.Data}
	}
	out := &CodeAction{
		Title:       a.Title,
		Kind:        a.Kind,
		Edit:        a.Edit,
		Diagnostics: a.Diagnostics,
		IsPreferred: a.IsPreferred,
		Disabled:    a.Disabled,
		Data:        a.Data,
	}
	if a.Command != "" {
		out.Command = &Command{Title: a.Title, Command: a.Command, Arguments: a.Arguments}
	}
	return out
}

// WorkspaceEdit groups edits across multiple documents.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []TextDocumentEdit    `json:"documentChanges,omitempty"`
}

// TextDocumentEdit is the per-document grouping in a WorkspaceEdit.
type TextDocumentEdit struct {
	TextDocument VersionedTextDocumentIdentifier `json:"textDocument"`
	Edits        []TextEdit                      `json:"edits"`
}

// VersionedTextDocumentIdentifier carries a version for optimistic edits.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextEdit is one textual change in a document.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// ApplyWorkspaceEditParams is the params of workspace/applyEdit.
type ApplyWorkspaceEditParams struct {
	Label string        `json:"label,omitempty"`
	Edit  WorkspaceEdit `json:"edit"`
}

// ApplyWorkspaceEditResponse is the server's reply (we send these as a
// reverse request — the server sends them to the client when it wants
// to apply edits via a command).
type ApplyWorkspaceEditResponse struct {
	Applied       bool   `json:"applied"`
	FailureReason string `json:"failureReason,omitempty"`
}

// ExecuteCommandParams is the params of workspace/executeCommand.
type ExecuteCommandParams struct {
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// DidChangeTextDocumentParams is sent when a document version moves.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// TextDocumentContentChangeEvent is a single edit to a document.
// When Range is nil the change replaces the whole text.
type TextDocumentContentChangeEvent struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

// DidCloseTextDocumentParams is sent on textDocument/didClose.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

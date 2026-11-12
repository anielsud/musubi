package mcpserver

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/store"
)

// The write tools record the owner's word, as the app's buttons do, and
// each can be undone. A correction re-judges the statement's links and
// rewrites the affected thread statuses with the configured models, the
// same small run the app makes.
func (s *Server) writeTools() []tool {
	return []tool{
		define(s, &mcp.Tool{Name: "judge", Annotations: recording("Judge a statement", true),
			Description: "Record the owner's verdict on the claim a statement belongs to: verified, disputed or false; \"clear\" removes it (the undo). Only when the owner asked."}, s.judge),
		define(s, &mcp.Tool{Name: "correct_statement", Annotations: recording("Correct a statement", false),
			Description: "Correct a statement's text or who it belongs to (speech recognition gets names and numbers wrong). Its links are judged again and its threads' statuses rewritten. Returns edit ids for undo_correction. Only when the owner asked."}, s.correct),
		define(s, &mcp.Tool{Name: "undo_correction", Annotations: recording("Undo a correction", true),
			Description: "Put a statement back as it was before a correction (by the edit id correct_statement returned)."}, s.undoCorrection),
		define(s, &mcp.Tool{Name: "mark_todo", Annotations: recording("Mark a to-do", false),
			Description: "Record the owner's word on a to-do (by its statement id from todos or meeting): done, dropped, not (not a to-do), due (note: the date as 2006-01-02, empty for none); or link it to another to-do: same, replaces, part (a step of other). Returns a mark id for undo_todo_mark. Only when the owner asked."}, s.markToDo),
		define(s, &mcp.Tool{Name: "undo_todo_mark", Annotations: recording("Undo a to-do mark", true),
			Description: "Undo one of the owner's to-do marks or links (by the mark id mark_todo returned)."}, s.undoMark),
	}
}

func (s *Server) knowledgeOpts() pipeline.KnowledgeOptions {
	return pipeline.KnowledgeOptions{LLM: s.e.Chat, Embedder: s.e.Embedder}
}

// statementExists checks an id before writing: the store would accept a
// verdict on nothing silently.
func (s *Server) statementExists(id int64) error {
	if id <= 0 {
		return fmt.Errorf("give a statement id")
	}
	m, err := s.e.Store.StatementsByID([]int64{id})
	if err != nil {
		return err
	}
	if _, ok := m[id]; !ok {
		return fmt.Errorf("no statement %d", id)
	}
	return nil
}

// --- judge ---

type JudgeIn struct {
	StatementID int64  `json:"statementId" jsonschema:"the statement's id"`
	Verdict     string `json:"verdict" jsonschema:"verified, disputed, false, or clear to remove the verdict"`
	Note        string `json:"note,omitempty" jsonschema:"why, in the owner's words"`
}

func (s *Server) judge(_ context.Context, in JudgeIn) (*result, error) {
	if err := s.statementExists(in.StatementID); err != nil {
		return nil, err
	}
	v := strings.ToLower(strings.TrimSpace(in.Verdict))
	if v == "clear" {
		v = ""
	} else if v != "verified" && v != "disputed" && v != "false" {
		return nil, fmt.Errorf("verdict: verified, disputed, false or clear")
	}
	if len(in.Note) > maxQuery {
		return nil, fmt.Errorf("note: at most %d characters", maxQuery)
	}
	if err := s.e.Store.Judge(in.StatementID, v, strings.TrimSpace(in.Note)); err != nil {
		return nil, err
	}
	if v == "" {
		return &result{body: map[string]any{"statementId": in.StatementID, "verdict": nil}, summary: "Verdict cleared."}, nil
	}
	return &result{body: map[string]any{"statementId": in.StatementID, "verdict": v, "undo": "judge with verdict clear"}, summary: "Verdict recorded."}, nil
}

// --- correct_statement ---

type CorrectIn struct {
	StatementID int64    `json:"statementId" jsonschema:"the statement's id"`
	Text        string   `json:"text,omitempty" jsonschema:"the corrected text; empty keeps it"`
	Who         []string `json:"who,omitempty" jsonschema:"who it belongs to, corrected; empty keeps them"`
}

func (s *Server) correct(ctx context.Context, in CorrectIn) (*result, error) {
	if err := s.statementExists(in.StatementID); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	var who []string
	for _, w := range in.Who {
		if w = strings.TrimSpace(w); w != "" {
			who = append(who, w)
		}
	}
	if text == "" && who == nil {
		return nil, fmt.Errorf("give corrected text, who, or both")
	}
	if len(text) > maxQuery || len(who) > maxRecipients {
		return nil, fmt.Errorf("text or who too long")
	}
	r, err := s.e.Runner(nil, io.Discard)
	if err != nil {
		return nil, err
	}
	start := time.Now().Add(-time.Second)
	if _, err := r.CorrectStatement(ctx, s.knowledgeOpts(), in.StatementID, text, who); err != nil {
		return nil, err
	}
	edits, err := s.e.Store.StatementEdits()
	if err != nil {
		return nil, err
	}
	var made []store.StatementEdit
	for _, e := range edits {
		if e.Statement == in.StatementID && !e.At.Before(start) {
			made = append(made, e)
		}
	}
	if len(made) == 0 {
		return &result{body: map[string]any{"statementId": in.StatementID, "edits": []any{}}, summary: "Nothing changed: the statement already reads that way."}, nil
	}
	return &result{body: map[string]any{"statementId": in.StatementID, "edits": made, "undo": "undo_correction with each edit's id"},
		summary: fmt.Sprintf("%d edits recorded; links re-judged and statuses rewritten.", len(made))}, nil
}

// --- undo_correction ---

type UndoCorrectionIn struct {
	EditID int64 `json:"editId" jsonschema:"the edit's id, from correct_statement"`
}

func (s *Server) undoCorrection(ctx context.Context, in UndoCorrectionIn) (*result, error) {
	if in.EditID <= 0 {
		return nil, fmt.Errorf("give an edit id")
	}
	r, err := s.e.Runner(nil, io.Discard)
	if err != nil {
		return nil, err
	}
	if err := r.UndoStatementEdit(ctx, s.knowledgeOpts(), in.EditID); err != nil {
		return nil, err
	}
	return &result{body: map[string]any{"editId": in.EditID, "undone": true}, summary: "Correction undone."}, nil
}

// --- mark_todo ---

type MarkIn struct {
	StatementID int64  `json:"statementId" jsonschema:"the to-do's statement id"`
	Mark        string `json:"mark" jsonschema:"done, dropped, not, due, same, replaces or part"`
	Other       int64  `json:"other,omitempty" jsonschema:"for same, replaces, part: the other to-do's statement id"`
	Note        string `json:"note,omitempty" jsonschema:"for due: the date as 2006-01-02 (empty for none); otherwise why"`
}

func (s *Server) markToDo(_ context.Context, in MarkIn) (*result, error) {
	if err := s.statementExists(in.StatementID); err != nil {
		return nil, fmt.Errorf("%w (a to-do with a negative id isn't matched to a statement yet, so it can't be marked)", err)
	}
	rel := strings.ToLower(strings.TrimSpace(in.Mark))
	if !store.OwnerLinkRels[rel] {
		return nil, fmt.Errorf("mark: done, dropped, not, due, same, replaces or part")
	}
	if in.Other != 0 {
		if err := s.statementExists(in.Other); err != nil {
			return nil, fmt.Errorf("other: %w", err)
		}
	}
	note := strings.TrimSpace(in.Note)
	if rel == "due" && note != "" {
		if _, err := time.Parse("2006-01-02", note); err != nil {
			return nil, fmt.Errorf("note: for due, the date as 2006-01-02")
		}
	}
	if len(note) > maxQuery {
		return nil, fmt.Errorf("note: at most %d characters", maxQuery)
	}
	id, err := s.e.Store.LinkStatements(in.StatementID, rel, in.Other, note, "owner")
	if err != nil {
		return nil, err
	}
	return &result{body: map[string]any{"markId": id, "statementId": in.StatementID, "mark": rel, "undo": "undo_todo_mark with this markId"}, summary: "Mark recorded."}, nil
}

// --- undo_todo_mark ---

type UndoMarkIn struct {
	MarkID int64 `json:"markId" jsonschema:"the mark's id, from mark_todo"`
}

func (s *Server) undoMark(_ context.Context, in UndoMarkIn) (*result, error) {
	if in.MarkID <= 0 {
		return nil, fmt.Errorf("give a mark id")
	}
	if err := s.e.Store.UndoOwnerLink(in.MarkID); err != nil {
		return nil, err
	}
	return &result{body: map[string]any{"markId": in.MarkID, "undone": true}, summary: "Mark undone."}, nil
}

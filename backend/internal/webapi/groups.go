package webapi

import (
	"net/http"

	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ListGroups(r.Context())
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.GetGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "GROUP_NOT_FOUND", "分组不存在")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, err := s.Store.CreateGroup(r.Context(), in.Name, in.Description)
	if err != nil {
		if err == store.ErrConflict {
			writeErr(w, http.StatusConflict, "GROUP_EXISTS", "分组名称已存在")
			return
		}
		writeErr(w, http.StatusBadRequest, "GROUP_INVALID", err.Error())
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "create_group", "group", g.ID, in)
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.UpdateGroup(r.Context(), id, in.Name, in.Description); err != nil {
		if err == store.ErrConflict {
			writeErr(w, http.StatusConflict, "GROUP_EXISTS", "分组名称已存在")
			return
		}
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "update_group", "group", id, in)
	g, _ := s.Store.GetGroup(r.Context(), id)
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteGroup(r.Context(), id); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "delete_group", "group", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetGroupMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserIDs []string `json:"user_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.SetGroupMembers(r.Context(), id, in.UserIDs); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "set_group_members", "group", id, in)
	g, _ := s.Store.GetGroup(r.Context(), id)
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleSetGroupPrinters(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PrinterIDs []string `json:"printer_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.SetGroupPrinters(r.Context(), id, in.PrinterIDs); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "set_group_printers", "group", id, in)
	g, _ := s.Store.GetGroup(r.Context(), id)
	writeJSON(w, http.StatusOK, g)
}

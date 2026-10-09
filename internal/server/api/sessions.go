package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/robin/hop-proxy/internal/server/db"
)

// SessionsHandler 会话管理处理（#80，admin only）。
// 展示登录会话（管理登录 + 一次性授权流程）的状态、来源与使用统计，
// 并支持强制下线（级联失效其应用授权）。
type SessionsHandler struct {
	db *db.DB
}

func newSessionsHandler(database *db.DB) *SessionsHandler {
	return &SessionsHandler{db: database}
}

// List 登录会话列表（可选筛选 user_id / source / status）
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var userID int64
	if v := q.Get("user_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			userID = n
		}
	}
	source := q.Get("source")
	status := q.Get("status")

	sessions, err := h.db.ListAuthSessions(userID, source, status)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取会话列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取会话列表失败")
		return
	}
	if sessions == nil {
		sessions = []db.AuthSession{}
	}
	jsonOK(w, sessions)
}

// Users 会话筛选的用户下拉：拥有登录会话的用户（含管理员）
func (h *SessionsHandler) Users(w http.ResponseWriter, r *http.Request) {
	users, err := h.db.ListSessionUsers()
	if err != nil {
		slog.ErrorContext(r.Context(), "获取会话用户列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取用户列表失败")
		return
	}
	if users == nil {
		users = []db.SessionUser{}
	}
	jsonOK(w, users)
}

// Get 会话详情：会话信息 + 授权应用列表（含各应用请求统计）
func (h *SessionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的会话 ID")
		return
	}

	session, err := h.db.GetAuthSession(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "会话不存在")
		return
	}
	grants, err := h.db.ListSessionGrants(id)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取会话授权列表失败", "type", "api", "error", err, "session_id", id)
		jsonError(w, http.StatusInternalServerError, "获取会话授权列表失败")
		return
	}
	if grants == nil {
		grants = []db.SessionGrant{}
	}
	jsonOK(w, map[string]any{"session": session, "grants": grants})
}

// Revoke 强制下线会话（标记失效并级联其全部应用授权）
func (h *SessionsHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的会话 ID")
		return
	}

	if err := h.db.RevokeAuthSession(id); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, http.StatusNotFound, "会话不存在")
			return
		}
		slog.ErrorContext(r.Context(), "强制下线会话失败", "type", "api", "error", err, "session_id", id)
		jsonError(w, http.StatusInternalServerError, "强制下线失败")
		return
	}

	slog.InfoContext(r.Context(), "会话强制下线", "type", "api", "session_id", id)
	jsonMsg(w, "已强制下线")
}

// DeleteGrant 删除会话下的单条应用授权记录（仍有效的授权删除即撤销，应用 cookie 立即失效）
func (h *SessionsHandler) DeleteGrant(w http.ResponseWriter, r *http.Request) {
	sessionID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的会话 ID")
		return
	}
	grantRef, err := strconv.ParseInt(r.PathValue("ref"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的授权 ID")
		return
	}

	err = h.db.DeleteSessionGrant(sessionID, grantRef)
	switch {
	case err == sql.ErrNoRows:
		jsonError(w, http.StatusNotFound, "授权不存在")
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "删除应用授权失败", "type", "api", "error", err, "session_id", sessionID, "grant_ref", grantRef)
		jsonError(w, http.StatusInternalServerError, "删除失败")
		return
	}

	slog.InfoContext(r.Context(), "应用授权删除", "type", "api", "session_id", sessionID, "grant_ref", grantRef)
	jsonMsg(w, "已删除")
}

// Delete 删除已失效会话记录（其应用授权记录一并删除）。
// 有效会话不允许删除，需先强制下线。
func (h *SessionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的会话 ID")
		return
	}

	err = h.db.DeleteAuthSession(id)
	switch {
	case err == sql.ErrNoRows:
		jsonError(w, http.StatusNotFound, "会话不存在")
		return
	case err == db.ErrAuthSessionActive:
		jsonError(w, http.StatusBadRequest, "会话仍有效，请先强制下线")
		return
	case err == db.ErrAuthSessionGrantsActive:
		jsonError(w, http.StatusBadRequest, "会话下的应用授权仍有效，请先删除相关授权")
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "删除会话记录失败", "type", "api", "error", err, "session_id", id)
		jsonError(w, http.StatusInternalServerError, "删除失败")
		return
	}

	slog.InfoContext(r.Context(), "会话记录删除", "type", "api", "session_id", id)
	jsonMsg(w, "已删除")
}

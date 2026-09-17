package api

import (
	"errors"
	"net/http"

	"relayproxy/server/service"
)

func (r *Router) handleChangePassword(w http.ResponseWriter, req *http.Request) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "密码请求格式无效")
		return
	}
	cookie, err := requestSessionCookie(req)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
		return
	}
	if err := r.authService.ChangePassword(cookie.Value, body.CurrentPassword, body.NewPassword); err != nil {
		switch {
		case errors.Is(err, service.ErrAuthSessionExpired):
			clearSessionCookies(w, req)
			writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
		case errors.Is(err, service.ErrInvalidCurrentPassword):
			writeError(w, http.StatusBadRequest, "原密码不正确，请重新输入")
		case errors.Is(err, service.ErrInvalidNewPassword):
			writeError(w, http.StatusBadRequest, "新密码须为 8–128 个字符，不能全部为空白")
		case errors.Is(err, service.ErrPasswordUnchanged):
			writeError(w, http.StatusBadRequest, "新密码不能与原密码相同")
		case errors.Is(err, service.ErrRateLimited):
			w.Header().Set("Retry-After", "300")
			writeError(w, http.StatusTooManyRequests, "原密码连续输错次数过多，请 5 分钟后再试")
		default:
			writeError(w, http.StatusInternalServerError, "保存密码失败，请稍后重试")
		}
		return
	}
	clearSessionCookies(w, req)
	writeJSON(w, http.StatusOK, map[string]string{"message": "管理密码已修改，请使用新密码重新登录。"})
}

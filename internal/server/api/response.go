package api

import (
	"encoding/json"
	"net/http"
)

// 统一 JSON 响应

// maxJSONBodySize 管理 API JSON 请求体上限（1MiB），防止超大请求触发 OOM。
// 登录、设置、应用等接口的 payload 远小于此值；文件上传走代理不经此路径。
const maxJSONBodySize = 1 << 20

type response struct {
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	Extra   any    `json:"extra,omitempty"`
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response{Data: data})
}

func jsonMsg(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response{Message: msg})
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response{Error: msg})
}

func jsonErrorWithData(w http.ResponseWriter, status int, msg string, extra any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response{Error: msg, Extra: extra})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	// 限制请求体大小，防止超大 JSON 触发输入等比例的内存分配
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBodySize)
	return json.NewDecoder(r.Body).Decode(v)
}

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"skirental/internal/auth"
	"skirental/internal/config"
)

type Server struct {
	db     *sql.DB
	cfg    config.Config
	routes []route
}

func NewServer(database *sql.DB, cfg config.Config) *Server {
	s := &Server{db: database, cfg: cfg}
	os.MkdirAll(cfg.UploadDir, 0o755)
	s.registerRoutes()
	go s.sweepLoop() // 申诉期届满的通知自动转为默认确认并结算释放押金
	return s
}

// ---------- 极简路由（支持 {var} 段，兼容 Go 1.19） ----------

type route struct {
	method  string
	segs    []string
	handler http.HandlerFunc
}

type ctxKey string

var varsKey ctxKey = "vars"
var claimsKey ctxKey = "claims"

func (s *Server) add(method, pattern string, h http.HandlerFunc) {
	s.routes = append(s.routes, route{method, strings.Split(strings.Trim(pattern, "/"), "/"), h})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	// 上传文件服务
	if strings.HasPrefix(path, "uploads/") {
		http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.cfg.UploadDir))).ServeHTTP(w, r)
		return
	}
	segs := strings.Split(path, "/")
	for _, rt := range s.routes {
		if rt.method != r.Method || len(rt.segs) != len(segs) {
			continue
		}
		vars := map[string]string{}
		ok := true
		for i, p := range rt.segs {
			if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
				vars[p[1:len(p)-1]] = segs[i]
			} else if p != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			ctx := context.WithValue(r.Context(), varsKey, vars)
			rt.handler(w, r.WithContext(ctx))
			return
		}
	}
	writeError(w, http.StatusNotFound, "接口不存在")
}

func urlVar(r *http.Request, name string) int64 {
	vars, _ := r.Context().Value(varsKey).(map[string]string)
	id, _ := strconv.ParseInt(vars[name], 10, 64)
	return id
}

func currentUser(r *http.Request) auth.Claims {
	c, _ := r.Context().Value(claimsKey).(auth.Claims)
	return c
}

// ---------- 中间件 ----------

func (s *Server) withAuth(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	allowed := map[string]bool{}
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "请先登录")
				return
			}
			c, err := auth.ParseToken(s.cfg.JWTSecret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				writeError(w, http.StatusUnauthorized, "登录已失效")
				return
			}
			if len(allowed) > 0 && !allowed[c.Role] {
				writeError(w, http.StatusForbidden, "当前角色无权操作")
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey, c)
			next(w, r.WithContext(ctx))
		}
	}
}

// ---------- JSON / DB 辅助 ----------

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// listJSON 用 json_agg 返回对象数组
func (s *Server) listJSON(w http.ResponseWriter, query string, args ...interface{}) {
	var data json.RawMessage
	err := s.db.QueryRow(query, args...).Scan(&data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, data)
}

// oneJSON 用 row_to_json 返回单行，找不到时 404
func (s *Server) oneJSON(w http.ResponseWriter, query string, args ...interface{}) {
	var data json.RawMessage
	err := s.db.QueryRow(query, args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) || string(data) == "null" {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// execOne 执行并要求至少影响一行
func (s *Server) execOne(w http.ResponseWriter, query string, args ...interface{}) (int64, bool) {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return 0, false
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusConflict, "状态已变化或记录不存在，刷新后重试")
		return 0, false
	}
	return n, true
}

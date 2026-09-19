package services

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

// A handler panic must come back as a 500 AND land in logrus — the log the
// error counters read — with the request that caused it.
func TestRecoverToLog(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.CustomRecovery(recoverToLog))
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom?limit=1", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	e := hook.LastEntry()
	if e == nil || e.Level != log.ErrorLevel || e.Message != "panic recovered" {
		t.Fatalf("expected a logrus error 'panic recovered', got %+v", e)
	}
	if e.Data["panic"] != "kaboom" || e.Data["path"] != "/boom" || e.Data["query"] != "limit=1" {
		t.Errorf("fields = %+v", e.Data)
	}
}

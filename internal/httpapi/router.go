package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"queuedesk/internal/booking"
)

type API struct {
	Service    *booking.Service
	Redis      *redis.Client
	AdminToken string
	Ready      func(context.Context) error
}

func (a *API) Router() *gin.Engine {
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.Use(gin.Logger(), gin.Recovery(), func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/health/live", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/health/ready", func(c *gin.Context) {
		if err := a.Ready(c.Request.Context()); err != nil {
			c.JSON(503, gin.H{"error": "зависимости недоступны"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.GET("/openapi.yaml", func(c *gin.Context) { c.File("docs/openapi.yaml") })
	v1 := r.Group("/api/v1", a.rateLimit)
	v1.GET("/events", a.listEvents)
	v1.POST("/events", a.admin, a.createEvent)
	v1.POST("/reservations", a.reserve)
	v1.GET("/reservations/:id", a.validID, a.getReservation)
	v1.POST("/reservations/:id/confirm", a.validID, a.changeStatus("confirmed"))
	v1.POST("/reservations/:id/cancel", a.validID, a.changeStatus("cancelled"))
	return r
}

func (a *API) admin(c *gin.Context) {
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+a.AdminToken)) != 1 {
		c.AbortWithStatusJSON(401, gin.H{"error": "нужен токен администратора"})
		return
	}
	c.Next()
}

func (a *API) validID(c *gin.Context) {
	id := c.Param("id")
	if len(id) != 36 {
		c.AbortWithStatusJSON(400, gin.H{"error": "некорректный UUID"})
		return
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				c.AbortWithStatusJSON(400, gin.H{"error": "некорректный UUID"})
				return
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			c.AbortWithStatusJSON(400, gin.H{"error": "некорректный UUID"})
			return
		}
	}
	c.Next()
}

func (a *API) createEvent(c *gin.Context) {
	var e booking.Event
	if err := c.ShouldBindJSON(&e); err != nil {
		c.JSON(400, gin.H{"error": "неверный JSON"})
		return
	}
	result, err := a.Service.CreateEvent(c.Request.Context(), e)
	if err != nil {
		respondError(c, err)
		return
	}
	// Кеш краткоживущий. Удаление популярных страниц ускоряет появление нового события.
	if a.Redis != nil {
		a.Redis.Del(c.Request.Context(), "events:20:0")
	}
	c.JSON(201, result)
}

func (a *API) listEvents(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		respondError(c, booking.ErrInvalid)
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || limit < 1 || limit > 100 || offset < 0 {
		respondError(c, booking.ErrInvalid)
		return
	}
	key := fmt.Sprintf("events:%d:%d", limit, offset)
	if a.Redis != nil {
		if cached, err := a.Redis.Get(c.Request.Context(), key).Bytes(); err == nil {
			c.Data(200, "application/json; charset=utf-8", cached)
			return
		}
	}
	items, err := a.Service.ListEvents(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	data := gin.H{"items": items, "limit": limit, "offset": offset}
	if a.Redis != nil {
		if raw, err := json.Marshal(data); err == nil {
			a.Redis.Set(c.Request.Context(), key, raw, 15*time.Second)
		}
	}
	c.JSON(200, data)
}

func (a *API) reserve(c *gin.Context) {
	var input struct {
		EventID int64  `json:"event_id"`
		Name    string `json:"name"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(400, gin.H{"error": "неверный JSON"})
		return
	}
	r, err := a.Service.Reserve(c.Request.Context(), input.EventID, input.Name, c.GetHeader("Idempotency-Key"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(201, r)
}

func (a *API) getReservation(c *gin.Context) {
	r, err := a.Service.GetReservation(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, r)
}
func (a *API) changeStatus(status string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, err := a.Service.ChangeStatus(c.Request.Context(), c.Param("id"), status)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(200, r)
	}
}

func respondError(c *gin.Context, err error) {
	status := 500
	message := "внутренняя ошибка сервера"
	switch {
	case errors.Is(err, booking.ErrInvalid):
		status = 400
		message = err.Error()
	case errors.Is(err, booking.ErrNotFound):
		status = 404
		message = err.Error()
	case errors.Is(err, booking.ErrConflict), errors.Is(err, booking.ErrSoldOut):
		status = 409
		message = err.Error()
	default:
		slog.Error("ошибка обработки запроса", "error", err)
	}
	c.JSON(status, gin.H{"error": message})
}

var limiter = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return count
`)

func (a *API) rateLimit(c *gin.Context) {
	if a.Redis == nil {
		c.Next()
		return
	}
	count, err := limiter.Run(c.Request.Context(), a.Redis, []string{"rate:" + c.ClientIP()}, 60).Int64()
	// При сбое Redis бронирование продолжает работать: целостность хранится в PostgreSQL.
	if err != nil {
		slog.Warn("лимитер Redis недоступен", "error", err)
		c.Next()
		return
	}
	if count > 200 {
		c.Header("Retry-After", "60")
		c.AbortWithStatusJSON(429, gin.H{"error": "слишком много запросов, повторите через минуту"})
		return
	}
	c.Next()
}

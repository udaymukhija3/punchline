package httpapi

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"punchline/backend/internal/realtime"
)

type metrics struct {
	mu sync.Mutex

	httpRequests map[httpMetricKey]uint64
	httpDuration map[httpMetricKey]durationMetric
	roomsCreated map[string]uint64
	wsMessages   map[wsMessageKey]uint64
	actionErrors map[string]uint64
	rateLimited  map[string]uint64

	wsConnectionsTotal uint64
	wsActive           int64
}

type httpMetricKey struct {
	Method string
	Route  string
	Status int
}

type wsMessageKey struct {
	Type   string
	Result string
}

type durationMetric struct {
	Count uint64
	Sum   float64
}

func newMetrics() *metrics {
	return &metrics{
		httpRequests: map[httpMetricKey]uint64{},
		httpDuration: map[httpMetricKey]durationMetric{},
		roomsCreated: map[string]uint64{},
		wsMessages:   map[wsMessageKey]uint64{},
		actionErrors: map[string]uint64{},
		rateLimited:  map[string]uint64{},
	}
}

func (m *metrics) recordHTTPRequest(method, route string, status int, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := httpMetricKey{Method: method, Route: route, Status: status}
	m.httpRequests[key]++
	d := m.httpDuration[key]
	d.Count++
	d.Sum += elapsed.Seconds()
	m.httpDuration[key] = d
}

func (m *metrics) recordRoomCreated(mode string) {
	if mode == "" {
		mode = "friends"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roomsCreated[mode]++
}

func (m *metrics) recordWSConnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsConnectionsTotal++
	m.wsActive++
}

func (m *metrics) recordWSDisconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wsActive > 0 {
		m.wsActive--
	}
}

func (m *metrics) recordWSMessage(messageType, result string) {
	if messageType == "" {
		messageType = "unknown"
	}
	if result == "" {
		result = "ok"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsMessages[wsMessageKey{Type: messageType, Result: result}]++
}

func (m *metrics) recordActionError(messageType string) {
	if messageType == "" {
		messageType = "unknown"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actionErrors[messageType]++
}

func (m *metrics) recordRateLimited(scope string) {
	if scope == "" {
		scope = "unknown"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimited[scope]++
}

func (m *metrics) writePrometheus(w http.ResponseWriter, stats realtime.RoomManagerStats) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintln(w, "# HELP punchline_http_requests_total HTTP requests handled by the Punchline server.")
	fmt.Fprintln(w, "# TYPE punchline_http_requests_total counter")
	for _, key := range sortedHTTPKeys(m.httpRequests) {
		fmt.Fprintf(w, "punchline_http_requests_total{method=%q,route=%q,status=%q} %d\n",
			key.Method, key.Route, strconv.Itoa(key.Status), m.httpRequests[key])
	}

	fmt.Fprintln(w, "# HELP punchline_http_request_duration_seconds Request duration summary by route and status.")
	fmt.Fprintln(w, "# TYPE punchline_http_request_duration_seconds summary")
	for _, key := range sortedDurationKeys(m.httpDuration) {
		d := m.httpDuration[key]
		fmt.Fprintf(w, "punchline_http_request_duration_seconds_sum{method=%q,route=%q,status=%q} %.6f\n",
			key.Method, key.Route, strconv.Itoa(key.Status), d.Sum)
		fmt.Fprintf(w, "punchline_http_request_duration_seconds_count{method=%q,route=%q,status=%q} %d\n",
			key.Method, key.Route, strconv.Itoa(key.Status), d.Count)
	}

	fmt.Fprintln(w, "# HELP punchline_rooms_created_total Rooms created by mode.")
	fmt.Fprintln(w, "# TYPE punchline_rooms_created_total counter")
	for _, mode := range sortedStringKeys(m.roomsCreated) {
		fmt.Fprintf(w, "punchline_rooms_created_total{mode=%q} %d\n", mode, m.roomsCreated[mode])
	}

	fmt.Fprintln(w, "# HELP punchline_ws_connections_total WebSocket connections accepted.")
	fmt.Fprintln(w, "# TYPE punchline_ws_connections_total counter")
	fmt.Fprintf(w, "punchline_ws_connections_total %d\n", m.wsConnectionsTotal)
	fmt.Fprintln(w, "# HELP punchline_ws_active_connections Active WebSocket connections.")
	fmt.Fprintln(w, "# TYPE punchline_ws_active_connections gauge")
	fmt.Fprintf(w, "punchline_ws_active_connections %d\n", m.wsActive)

	fmt.Fprintln(w, "# HELP punchline_ws_messages_total WebSocket client messages by type and result.")
	fmt.Fprintln(w, "# TYPE punchline_ws_messages_total counter")
	for _, key := range sortedWSKeys(m.wsMessages) {
		fmt.Fprintf(w, "punchline_ws_messages_total{type=%q,result=%q} %d\n", key.Type, key.Result, m.wsMessages[key])
	}

	fmt.Fprintln(w, "# HELP punchline_room_action_errors_total Room action errors by client message type.")
	fmt.Fprintln(w, "# TYPE punchline_room_action_errors_total counter")
	for _, messageType := range sortedStringKeys(m.actionErrors) {
		fmt.Fprintf(w, "punchline_room_action_errors_total{type=%q} %d\n", messageType, m.actionErrors[messageType])
	}

	fmt.Fprintln(w, "# HELP punchline_rate_limited_total Requests or messages rejected by local rate limits.")
	fmt.Fprintln(w, "# TYPE punchline_rate_limited_total counter")
	for _, scope := range sortedStringKeys(m.rateLimited) {
		fmt.Fprintf(w, "punchline_rate_limited_total{scope=%q} %d\n", scope, m.rateLimited[scope])
	}

	fmt.Fprintln(w, "# HELP punchline_rooms_local Rooms currently hosted by this instance.")
	fmt.Fprintln(w, "# TYPE punchline_rooms_local gauge")
	fmt.Fprintf(w, "punchline_rooms_local{instance=%q,registry=%q} %d\n", stats.InstanceID, stats.RoomRegistry, stats.LocalRoomCount)
	fmt.Fprintln(w, "# HELP punchline_players_connected Human players currently connected to rooms on this instance.")
	fmt.Fprintln(w, "# TYPE punchline_players_connected gauge")
	fmt.Fprintf(w, "punchline_players_connected %d\n", stats.ConnectedPlayers)
	fmt.Fprintln(w, "# HELP punchline_instance_draining Whether this instance is intentionally leaving service.")
	fmt.Fprintln(w, "# TYPE punchline_instance_draining gauge")
	fmt.Fprintf(w, "punchline_instance_draining %d\n", boolGauge(stats.Draining))
	fmt.Fprintln(w, "# HELP punchline_room_lease_ttl_seconds Configured room lease TTL.")
	fmt.Fprintln(w, "# TYPE punchline_room_lease_ttl_seconds gauge")
	fmt.Fprintf(w, "punchline_room_lease_ttl_seconds %.0f\n", stats.RoomLeaseTTL.Seconds())

	fmt.Fprintln(w, "# HELP punchline_registry_operations_total Room registry operations by operation and result.")
	fmt.Fprintln(w, "# TYPE punchline_registry_operations_total counter")
	for _, op := range stats.RegistryOperations {
		fmt.Fprintf(w, "punchline_registry_operations_total{operation=%q,result=%q} %d\n", op.Operation, op.Result, op.Count)
	}
	if stats.Database != nil {
		fmt.Fprintln(w, "# HELP punchline_database_max_open_connections Configured maximum number of database connections.")
		fmt.Fprintln(w, "# TYPE punchline_database_max_open_connections gauge")
		fmt.Fprintf(w, "punchline_database_max_open_connections %d\n", stats.Database.MaxOpenConnections)
		fmt.Fprintln(w, "# HELP punchline_database_open_connections Open database connections.")
		fmt.Fprintln(w, "# TYPE punchline_database_open_connections gauge")
		fmt.Fprintf(w, "punchline_database_open_connections %d\n", stats.Database.OpenConnections)
		fmt.Fprintln(w, "# HELP punchline_database_connections_in_use Database connections currently in use.")
		fmt.Fprintln(w, "# TYPE punchline_database_connections_in_use gauge")
		fmt.Fprintf(w, "punchline_database_connections_in_use %d\n", stats.Database.InUse)
		fmt.Fprintln(w, "# HELP punchline_database_connections_idle Idle database connections.")
		fmt.Fprintln(w, "# TYPE punchline_database_connections_idle gauge")
		fmt.Fprintf(w, "punchline_database_connections_idle %d\n", stats.Database.Idle)
		fmt.Fprintln(w, "# HELP punchline_database_wait_count Total waits for a database connection.")
		fmt.Fprintln(w, "# TYPE punchline_database_wait_count counter")
		fmt.Fprintf(w, "punchline_database_wait_count %d\n", stats.Database.WaitCount)
		fmt.Fprintln(w, "# HELP punchline_database_wait_duration_seconds Total time spent waiting for a database connection.")
		fmt.Fprintln(w, "# TYPE punchline_database_wait_duration_seconds counter")
		fmt.Fprintf(w, "punchline_database_wait_duration_seconds %.6f\n", stats.Database.WaitDuration.Seconds())
	}
	fmt.Fprintln(w, "# HELP punchline_registry_operation_duration_seconds Room registry operation duration summary by operation and result.")
	fmt.Fprintln(w, "# TYPE punchline_registry_operation_duration_seconds summary")
	for _, op := range stats.RegistryOperations {
		fmt.Fprintf(w, "punchline_registry_operation_duration_seconds_sum{operation=%q,result=%q} %.6f\n", op.Operation, op.Result, op.DurationSeconds)
		fmt.Fprintf(w, "punchline_registry_operation_duration_seconds_count{operation=%q,result=%q} %d\n", op.Operation, op.Result, op.Count)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintln(w, "# HELP punchline_go_goroutines Current number of goroutines.")
	fmt.Fprintln(w, "# TYPE punchline_go_goroutines gauge")
	fmt.Fprintf(w, "punchline_go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintln(w, "# HELP punchline_go_heap_alloc_bytes Current heap bytes allocated and still in use.")
	fmt.Fprintln(w, "# TYPE punchline_go_heap_alloc_bytes gauge")
	fmt.Fprintf(w, "punchline_go_heap_alloc_bytes %d\n", mem.HeapAlloc)
	fmt.Fprintln(w, "# HELP punchline_go_heap_sys_bytes Heap bytes obtained from the OS.")
	fmt.Fprintln(w, "# TYPE punchline_go_heap_sys_bytes gauge")
	fmt.Fprintf(w, "punchline_go_heap_sys_bytes %d\n", mem.HeapSys)
	fmt.Fprintln(w, "# HELP punchline_go_stack_inuse_bytes Stack bytes currently in use.")
	fmt.Fprintln(w, "# TYPE punchline_go_stack_inuse_bytes gauge")
	fmt.Fprintf(w, "punchline_go_stack_inuse_bytes %d\n", mem.StackInuse)
}

func routeLabel(r *http.Request) string {
	path := r.URL.Path
	switch {
	case path == "/":
		return "/"
	case path == "/healthz":
		return "/healthz"
	case path == "/readyz":
		return "/readyz"
	case path == "/metrics":
		return "/metrics"
	case path == "/api/computer-room":
		return "/api/computer-room"
	case path == "/api/rooms":
		return "/api/rooms"
	case strings.HasPrefix(path, "/api/rooms/") && strings.HasSuffix(strings.TrimRight(path, "/"), "/join"):
		return "/api/rooms/{code}/join"
	case strings.HasPrefix(path, "/api/rooms/"):
		return "/api/rooms/{code}"
	case strings.HasPrefix(path, "/ws/rooms/"):
		return "/ws/rooms/{code}"
	case strings.HasPrefix(path, "/assets/"):
		return "/assets/*"
	default:
		return "static"
	}
}

func boolGauge(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sortedHTTPKeys(values map[httpMetricKey]uint64) []httpMetricKey {
	keys := make([]httpMetricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return httpKeyLess(keys[i], keys[j])
	})
	return keys
}

func sortedDurationKeys(values map[httpMetricKey]durationMetric) []httpMetricKey {
	keys := make([]httpMetricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return httpKeyLess(keys[i], keys[j])
	})
	return keys
}

func httpKeyLess(a, b httpMetricKey) bool {
	if a.Route != b.Route {
		return a.Route < b.Route
	}
	if a.Method != b.Method {
		return a.Method < b.Method
	}
	return a.Status < b.Status
}

func sortedWSKeys(values map[wsMessageKey]uint64) []wsMessageKey {
	keys := make([]wsMessageKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Type != keys[j].Type {
			return keys[i].Type < keys[j].Type
		}
		return keys[i].Result < keys[j].Result
	})
	return keys
}

func sortedStringKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

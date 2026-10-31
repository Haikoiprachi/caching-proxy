// caching-proxy: ek chhota CLI tool jo requests ko origin server tak forward karta hai
// aur GET responses ko cache karta hai.
//
// Usage:
//   caching-proxy --port 3000 --origin http://dummyjson.com
//   caching-proxy --port 3000 --clear-cache
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const clearPath = "/__cache/clear"

// ---------------------------------------------------------------------------
// STEP 3: Cache (key -> saved response), mutex ke saath thread-safe
// ---------------------------------------------------------------------------

// cachedResponse ek saved response hai: status, headers aur body.
type cachedResponse struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

// Cache ek in-memory map hai. Mutex isliye ki kai requests ek saath aa sakti hain.
type Cache struct {
	mu    sync.RWMutex
	items map[string]cachedResponse
	ttl   time.Duration // 0 matlab kabhi expire nahi hoga
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{items: make(map[string]cachedResponse), ttl: ttl}
}

// Get cache se response nikalta hai. Expire ho chuka ho toh hata deta hai.
func (c *Cache) Get(key string) (cachedResponse, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return cachedResponse{}, false
	}
	if c.ttl > 0 && time.Now().After(item.expires) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return cachedResponse{}, false
	}
	return item, true
}

// Set response ko cache mein daalta hai.
func (c *Cache) Set(key string, item cachedResponse) {
	if c.ttl > 0 {
		item.expires = time.Now().Add(c.ttl)
	}
	c.mu.Lock()
	c.items[key] = item
	c.mu.Unlock()
}

// Clear poora cache khaali karta hai aur batata hai kitni entries hati.
func (c *Cache) Clear() int {
	c.mu.Lock()
	n := len(c.items)
	c.items = make(map[string]cachedResponse)
	c.mu.Unlock()
	return n
}

// ---------------------------------------------------------------------------
// STEP 2 + 4 + 5: Proxy (forward + cache + X-Cache header)
// ---------------------------------------------------------------------------

type Proxy struct {
	origin string // trailing slash ke bina, jaise "http://dummyjson.com"
	cache  *Cache
	client *http.Client
}

func NewProxy(origin string, cache *Cache) *Proxy {
	return &Proxy{
		origin: strings.TrimRight(origin, "/"),
		cache:  cache,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// Redirects ko khud follow mat karo, client ko wapas bhej do.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Ye headers sirf ek connection ke liye hote hain, inhe aage forward nahi karte.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		dst.Del(h)
	}
}

func writeResponse(w http.ResponseWriter, item cachedResponse, xcache string) {
	copyHeaders(w.Header(), item.header)
	w.Header().Set("Content-Length", strconv.Itoa(len(item.body)))
	w.Header().Set("X-Cache", xcache)
	w.WriteHeader(item.status)
	w.Write(item.body)
}

// ServeHTTP har incoming request ke liye chalta hai.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Cache clear karne ka special endpoint (--clear-cache isi ko call karta hai).
	if r.URL.Path == clearPath && r.Method == http.MethodDelete {
		n := p.cache.Clear()
		fmt.Fprintf(w, "cache cleared (%d entries removed)\n", n)
		return
	}

	// Cache key = method + path + query, jaise "GET /products?limit=10"
	key := r.Method + " " + r.URL.RequestURI()
	cacheable := r.Method == http.MethodGet // sirf GET cache karte hain

	// 1) Cache mein hai? Toh seedha wahi do.
	if cacheable {
		if item, ok := p.cache.Get(key); ok {
			log.Printf("HIT  %s", key)
			writeResponse(w, item, "HIT")
			return
		}
	}

	// 2) Nahi hai, toh origin server ko request bhejo.
	req, err := http.NewRequest(r.Method, p.origin+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	copyHeaders(req.Header, r.Header)
	// Accept-Encoding hata do, taaki Go khud gzip handle kare aur cache mein
	// hamesha plain body save ho.
	req.Header.Del("Accept-Encoding")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("ERR  %s -> %v", key, err)
		http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		return
	}

	item := cachedResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: body}

	// 3) Sirf successful (200) GET response cache karo.
	if cacheable && resp.StatusCode == http.StatusOK {
		p.cache.Set(key, item)
	}

	log.Printf("MISS %s (status %d)", key, resp.StatusCode)
	writeResponse(w, item, "MISS")
}

// ---------------------------------------------------------------------------
// STEP 1 + 6: CLI (flags) aur --clear-cache
// ---------------------------------------------------------------------------

// clearRemote chalte hue proxy ko cache clear karne ka request bhejta hai.
func clearRemote(port int) error {
	endpoint := fmt.Sprintf("http://localhost:%d%s", port, clearPath)
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("proxy se connect nahi hua (port %d pe chal raha hai?): %w", port, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	fmt.Print(string(msg))
	return nil
}

func main() {
	port := flag.Int("port", 3000, "port jis par proxy server chalega")
	origin := flag.String("origin", "", "origin server ka URL (jaise http://dummyjson.com)")
	clear := flag.Bool("clear-cache", false, "chalte hue proxy ka cache clear karo")
	ttl := flag.Duration("ttl", 0, "cache expiry, jaise 30s ya 5m (0 = kabhi expire nahi)")
	flag.Parse()

	if *clear {
		if err := clearRemote(*port); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if *origin == "" {
		fmt.Fprintln(os.Stderr, "error: --origin zaroori hai")
		flag.Usage()
		os.Exit(1)
	}
	u, err := url.Parse(*origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintln(os.Stderr, "error: --origin ek valid http/https URL hona chahiye")
		os.Exit(1)
	}

	proxy := NewProxy(*origin, NewCache(*ttl))
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Caching proxy chalu: http://localhost%s -> %s", addr, *origin)
	log.Fatal(http.ListenAndServe(addr, proxy))
}

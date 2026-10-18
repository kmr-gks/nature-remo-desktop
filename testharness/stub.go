//go:build ignore

// stub.go fakes both the Nature Cloud API and a Remo's Local API so the panel
// can be exercised end to end without touching real hardware or an account.
//
//	go run testharness/stub.go -cloud :9001 -local :9002
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var (
	mu       sync.Mutex
	acTemp   = "26"
	acMode   = "cool"
	acVol    = "auto"
	acDir    = "auto"
	acButton = ""
	calls    []string
	reqCount int
)

func record(f string, a ...any) {
	mu.Lock()
	calls = append(calls, fmt.Sprintf(f, a...))
	mu.Unlock()
	log.Printf("STUB "+f, a...)
}

func appliances() string {
	mu.Lock()
	defer mu.Unlock()
	return fmt.Sprintf(`[
 {"id":"ac-1","nickname":"リビング エアコン","image":"ico_ac_1","type":"AC",
  "device":{"id":"dev-1","name":"Remo リビング","newest_events":{
     "te":{"val":24.3,"created_at":"2026-10-10T00:00:00Z"},
     "hu":{"val":51,"created_at":"2026-10-10T00:00:00Z"},
     "il":{"val":87,"created_at":"2026-10-10T00:00:00Z"}}},
  "settings":{"temp":"%s","temp_unit":"c","mode":"%s","vol":"%s","dir":"%s","dirh":"","button":"%s",
              "updated_at":"2026-10-10T00:00:00Z"},
  "aircon":{"tempUnit":"c","range":{"fixedButtons":["power-off"],"modes":{
     "cool":{"temp":["20","21","22","23","24","25","26","27","28"],"vol":["auto","1","2","3"],"dir":["auto","1","2","swing"],"dirh":[]},
     "warm":{"temp":["18","19","20","21","22","23","24"],"vol":["auto","1","2"],"dir":["auto","1"],"dirh":[]},
     "dry":{"temp":[""],"vol":["auto"],"dir":["auto"],"dirh":[]}}}},
  "signals":[],"model":{"id":"m1","name":"AC"}},

 {"id":"light-1","nickname":"寝室の照明","image":"ico_light","type":"LIGHT",
  "device":{"id":"dev-1","name":"Remo リビング","newest_events":{}},
  "light":{"state":{"brightness":"","power":"on","last_button":"on"},
   "buttons":[{"name":"on","image":"ico_on","label":"Light_all"},
              {"name":"off","image":"ico_off","label":"Light_off"},
              {"name":"night","image":"ico_night","label":"Light_night"},
              {"name":"bright-up","image":"ico_up","label":"Light_bright_up"}]},
  "signals":[],"model":{"id":"m2","name":"Light"}},

 {"id":"tv-1","nickname":"テレビ","image":"ico_tv","type":"TV",
  "device":{"id":"dev-1","name":"Remo リビング","newest_events":{}},
  "tv":{"state":{"input":"t"},
   "buttons":[{"name":"power","image":"","label":""},
              {"name":"select-input-src","image":"","label":""},
              {"name":"tv-schedule","image":"","label":""},
              {"name":"mute","image":"","label":""},
              {"name":"input-terrestrial","image":"","label":""},
              {"name":"input-bs","image":"","label":""},
              {"name":"input-cs","image":"","label":""},
              {"name":"select-audio","image":"","label":""},
              {"name":"ch-1","image":"","label":""},
              {"name":"ch-2","image":"","label":""},
              {"name":"ch-3","image":"","label":""},
              {"name":"ch-4","image":"","label":""},
              {"name":"ch-5","image":"","label":""},
              {"name":"ch-6","image":"","label":""},
              {"name":"ch-7","image":"","label":""},
              {"name":"ch-8","image":"","label":""},
              {"name":"ch-9","image":"","label":""},
              {"name":"ch-10","image":"","label":""},
              {"name":"ch-11","image":"","label":""},
              {"name":"ch-12","image":"","label":""},
              {"name":"back","image":"","label":""},
              {"name":"home","image":"","label":""},
              {"name":"display","image":"","label":""},
              {"name":"d","image":"","label":""},
              {"name":"ch-up","image":"","label":""},
              {"name":"ch-down","image":"","label":""},
              {"name":"left","image":"","label":""},
              {"name":"up","image":"","label":""},
              {"name":"right","image":"","label":""},
              {"name":"down","image":"","label":""},
              {"name":"ok","image":"","label":""},
              {"name":"vol-up","image":"","label":""},
              {"name":"vol-down","image":"","label":""},
              {"name":"blue","image":"","label":""},
              {"name":"red","image":"","label":""},
              {"name":"green","image":"","label":""},
              {"name":"yellow","image":"","label":""},
              {"name":"fast-rewind","image":"","label":""},
              {"name":"play","image":"","label":""},
              {"name":"record","image":"","label":""},
              {"name":"fast-forward","image":"","label":""},
              {"name":"prev","image":"","label":""},
              {"name":"pause","image":"","label":""},
              {"name":"stop","image":"","label":""},
              {"name":"next","image":"","label":""},
              {"name":"rec-list","image":"","label":""},
              {"name":"program-info","image":"","label":""},
              {"name":"option","image":"","label":""},
              {"name":"rewind-10-sec","image":"","label":""},
              {"name":"forward-30-sec","image":"","label":""},
              {"name":"hulu","image":"","label":""}]},
  "signals":[],"model":{"id":"m3","name":"TV"}},

 {"id":"ir-1","nickname":"サーキュレーター","image":"ico_fan","type":"IR",
  "device":{"id":"dev-1","name":"Remo リビング","newest_events":{}},
  "signals":[{"id":"sig-on","name":"電源","image":"ico_io"},
             {"id":"sig-sw","name":"首振り","image":"ico_fan"}],
  "model":{"id":"m4","name":"IR"}}
]`, acTemp, acMode, acVol, acDir, acButton)
}

func main() {
	cloudAddr := flag.String("cloud", ":9001", "cloud stub address")
	localAddr := flag.String("local", ":9002", "Remo local stub address")
	flag.Parse()

	cloud := http.NewServeMux()
	cloud.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TEST-TOKEN" {
			record("UNAUTHORIZED %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
			return
		}
		mu.Lock()
		reqCount++
		n := reqCount
		mu.Unlock()
		w.Header().Set("X-Rate-Limit-Limit", "30")
		w.Header().Set("X-Rate-Limit-Remaining", strconv.Itoa(30-n))
		w.Header().Set("X-Rate-Limit-Reset", "1760000000")
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		_ = r.ParseForm()

		switch {
		case r.Method == "GET" && path == "/1/appliances":
			record("GET /1/appliances")
			fmt.Fprint(w, appliances())

		case r.Method == "POST" && strings.HasSuffix(path, "/aircon_settings"):
			mu.Lock()
			acTemp = r.PostForm.Get("temperature")
			acMode = r.PostForm.Get("operation_mode")
			acVol = r.PostForm.Get("air_volume")
			acDir = r.PostForm.Get("air_direction")
			acButton = r.PostForm.Get("button")
			out := fmt.Sprintf(`{"temp":"%s","temp_unit":"c","mode":"%s","vol":"%s","dir":"%s","dirh":"","button":"%s","updated_at":"2026-10-10T00:00:00Z"}`,
				acTemp, acMode, acVol, acDir, acButton)
			mu.Unlock()
			record("POST aircon_settings temp=%q mode=%q vol=%q dir=%q dirh=%q button=%q unit=%q",
				r.PostForm.Get("temperature"), r.PostForm.Get("operation_mode"),
				r.PostForm.Get("air_volume"), r.PostForm.Get("air_direction"),
				r.PostForm.Get("air_direction_h"), r.PostForm.Get("button"),
				r.PostForm.Get("temperature_unit"))
			fmt.Fprint(w, out)

		case r.Method == "POST" && strings.HasSuffix(path, "/light"):
			record("POST light button=%q ct=%q", r.PostForm.Get("button"), r.Header.Get("Content-Type"))
			fmt.Fprint(w, `{}`)

		case r.Method == "POST" && strings.HasSuffix(path, "/tv"):
			record("POST tv button=%q", r.PostForm.Get("button"))
			fmt.Fprint(w, `{}`)

		case r.Method == "POST" && strings.HasSuffix(path, "/send"):
			record("POST %s", path)
			fmt.Fprint(w, `{}`)

		case path == "/__429":
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"rate limited"}`)

		case path == "/__calls":
			mu.Lock()
			b, _ := json.Marshal(calls)
			mu.Unlock()
			w.Write(b)

		default:
			record("UNHANDLED %s %s", r.Method, path)
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	})

	local := http.NewServeMux()
	local.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") == "" {
			record("LOCAL missing X-Requested-With")
			http.Error(w, "X-Requested-With required", http.StatusBadRequest)
			return
		}
		if r.Method == "GET" {
			record("LOCAL GET /messages")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"freq":38,"data":[2523,2717,786,903,777,924,763,1964],"format":"us"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record("LOCAL POST /messages freq=%v len(data)=%d expect=%q",
			body["freq"], len(body["data"].([]any)), r.Header.Get("Expect"))
		w.WriteHeader(http.StatusOK)
	})

	go func() { log.Fatal(http.ListenAndServe(*localAddr, local)) }()
	log.Printf("cloud stub on %s, local stub on %s", *cloudAddr, *localAddr)
	log.Fatal(http.ListenAndServe(*cloudAddr, cloud))
}

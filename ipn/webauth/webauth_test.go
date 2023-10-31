// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package webauth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"tailscale.com/client/tailscale"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/memnet"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

func TestGetTailscaleBrowserSession(t *testing.T) {
	userA := &tailcfg.UserProfile{ID: tailcfg.UserID(1)}
	userB := &tailcfg.UserProfile{ID: tailcfg.UserID(2)}

	userANodeIP := "100.100.100.101"
	userBNodeIP := "100.100.100.102"
	taggedNodeIP := "100.100.100.103"

	var selfNode *ipnstate.PeerStatus
	tags := views.SliceOf([]string{"tag:server"})
	tailnetNodes := map[string]*apitype.WhoIsResponse{
		userANodeIP: {
			Node:        &tailcfg.Node{ID: 1, StableID: "1"},
			UserProfile: userA,
		},
		userBNodeIP: {
			Node:        &tailcfg.Node{ID: 2, StableID: "2"},
			UserProfile: userB,
		},
		taggedNodeIP: {
			Node: &tailcfg.Node{ID: 3, StableID: "3", Tags: tags.AsSlice()},
		},
	}

	lal := memnet.Listen("local-tailscaled.sock:80")
	defer lal.Close()
	localapi := mockLocalAPI(t, tailnetNodes, func() *ipnstate.PeerStatus { return selfNode })
	defer localapi.Close()
	go localapi.Serve(lal)

	s := &Server{
		timeNow: time.Now,
		lc:      &tailscale.LocalClient{Dial: lal.Dial},
	}

	// Add some browser sessions to cache state.
	userASession := &browserSession{
		ID:            "cookie1",
		SrcNode:       1,
		SrcUser:       userA.ID,
		Created:       time.Now(),
		Authenticated: false, // not yet authenticated
	}
	userBSession := &browserSession{
		ID:            "cookie2",
		SrcNode:       2,
		SrcUser:       userB.ID,
		Created:       time.Now().Add(-2 * sessionCookieExpiry),
		Authenticated: true, // expired
	}
	userASessionAuthorized := &browserSession{
		ID:            "cookie3",
		SrcNode:       1,
		SrcUser:       userA.ID,
		Created:       time.Now(),
		Authenticated: true, // authenticated and not expired
	}
	s.sessions.Store(userASession.ID, userASession)
	s.sessions.Store(userBSession.ID, userBSession)
	s.sessions.Store(userASessionAuthorized.ID, userASessionAuthorized)

	tests := []struct {
		name       string
		selfNode   *ipnstate.PeerStatus
		remoteAddr string
		cookie     string

		wantSession      *browserSession
		wantError        error
		wantIsAuthorized bool // response from session.isAuthorized
	}{
		{
			name:        "not-connected-over-tailscale",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:  "77.77.77.77",
			wantSession: nil,
			wantError:   errNotUsingTailscale,
		},
		{
			name:        "no-session-user-self-node",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:  userANodeIP,
			cookie:      "not-a-cookie",
			wantSession: nil,
			wantError:   errNoSession,
		},
		{
			name:        "no-session-tagged-self-node",
			selfNode:    &ipnstate.PeerStatus{ID: "self", Tags: &tags},
			remoteAddr:  userANodeIP,
			wantSession: nil,
			wantError:   errNoSession,
		},
		{
			name:        "not-owner",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:  userBNodeIP,
			wantSession: nil,
			wantError:   errNotOwner,
		},
		{
			name:        "tagged-remote-source",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:  taggedNodeIP,
			wantSession: nil,
			wantError:   errTaggedRemoteSource,
		},
		{
			name:        "tagged-local-source",
			selfNode:    &ipnstate.PeerStatus{ID: "3"},
			remoteAddr:  taggedNodeIP, // same node as selfNode
			wantSession: nil,
			wantError:   errTaggedLocalSource,
		},
		{
			name:        "not-tagged-local-source",
			selfNode:    &ipnstate.PeerStatus{ID: "1", UserID: userA.ID},
			remoteAddr:  userANodeIP, // same node as selfNode
			cookie:      userASession.ID,
			wantSession: userASession,
			wantError:   nil, // should not error
		},
		{
			name:        "has-session",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:  userANodeIP,
			cookie:      userASession.ID,
			wantSession: userASession,
			wantError:   nil,
		},
		{
			name:             "has-authorized-session",
			selfNode:         &ipnstate.PeerStatus{ID: "self", UserID: userA.ID},
			remoteAddr:       userANodeIP,
			cookie:           userASessionAuthorized.ID,
			wantSession:      userASessionAuthorized,
			wantError:        nil,
			wantIsAuthorized: true,
		},
		{
			name:        "session-associated-with-different-source",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userB.ID},
			remoteAddr:  userBNodeIP,
			cookie:      userASession.ID,
			wantSession: nil,
			wantError:   errNoSession,
		},
		{
			name:        "session-expired",
			selfNode:    &ipnstate.PeerStatus{ID: "self", UserID: userB.ID},
			remoteAddr:  userBNodeIP,
			cookie:      userBSession.ID,
			wantSession: nil,
			wantError:   errNoSession,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selfNode = tt.selfNode
			r := &http.Request{RemoteAddr: tt.remoteAddr, Header: http.Header{}}
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tt.cookie})
			}
			session, _, err := s.getTailscaleBrowserSession(r)
			if !errors.Is(err, tt.wantError) {
				t.Errorf("wrong error; want=%v, got=%v", tt.wantError, err)
			}
			if diff := cmp.Diff(session, tt.wantSession); diff != "" {
				t.Errorf("wrong session; (-got+want):%v", diff)
			}
			if gotIsAuthorized := session.isAuthorized(s.timeNow()); gotIsAuthorized != tt.wantIsAuthorized {
				t.Errorf("wrong isAuthorized; want=%v, got=%v", tt.wantIsAuthorized, gotIsAuthorized)
			}
		})
	}
}

func TestServeTailscaleAuth(t *testing.T) {
	user := &tailcfg.UserProfile{ID: tailcfg.UserID(1)}
	self := &ipnstate.PeerStatus{ID: "self", UserID: user.ID}
	remoteNode := &apitype.WhoIsResponse{Node: &tailcfg.Node{ID: 1}, UserProfile: user}
	remoteIP := "100.100.100.101"

	lal := memnet.Listen("local-tailscaled.sock:80")
	defer lal.Close()
	localapi := mockLocalAPI(t,
		map[string]*apitype.WhoIsResponse{remoteIP: remoteNode},
		func() *ipnstate.PeerStatus { return self },
	)
	defer localapi.Close()
	go localapi.Serve(lal)

	timeNow := time.Now()
	oneHourAgo := timeNow.Add(-time.Hour)
	sixtyDaysAgo := timeNow.Add(-sessionCookieExpiry * 2)

	s := &Server{
		lc:      &tailscale.LocalClient{Dial: lal.Dial},
		timeNow: func() time.Time { return timeNow },
	}

	successCookie := "ts-cookie-success"
	s.sessions.Store(successCookie, &browserSession{
		ID:      successCookie,
		SrcNode: remoteNode.Node.ID,
		SrcUser: user.ID,
		Created: oneHourAgo,
		AuthID:  testAuthPathSuccess,
		AuthURL: testControlURL + testAuthPathSuccess,
	})
	failureCookie := "ts-cookie-failure"
	s.sessions.Store(failureCookie, &browserSession{
		ID:      failureCookie,
		SrcNode: remoteNode.Node.ID,
		SrcUser: user.ID,
		Created: oneHourAgo,
		AuthID:  testAuthPathError,
		AuthURL: testControlURL + testAuthPathError,
	})
	expiredCookie := "ts-cookie-expired"
	s.sessions.Store(expiredCookie, &browserSession{
		ID:      expiredCookie,
		SrcNode: remoteNode.Node.ID,
		SrcUser: user.ID,
		Created: sixtyDaysAgo,
		AuthID:  "/a/old-auth-url",
		AuthURL: testControlURL + "/a/old-auth-url",
	})

	tests := []struct {
		name          string
		cookie        string
		query         string
		wantStatus    int
		wantResp      *LoginResponse
		wantNewCookie bool            // new cookie generated
		wantSession   *browserSession // session associated w/ cookie at end of request
	}{
		{
			name:          "new-session-created",
			wantStatus:    http.StatusOK,
			wantResp:      &LoginResponse{OK: false, AuthURL: testControlURL + testAuthPath},
			wantNewCookie: true,
			wantSession: &browserSession{
				ID:            "GENERATED_ID", // gets swapped for newly created ID by test
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       timeNow,
				AuthID:        testAuthPath,
				AuthURL:       testControlURL + testAuthPath,
				Authenticated: false,
			},
		},
		{
			name:       "query-existing-incomplete-session",
			cookie:     successCookie,
			wantStatus: http.StatusOK,
			wantResp:   &LoginResponse{OK: false, AuthURL: testControlURL + testAuthPathSuccess},
			wantSession: &browserSession{
				ID:            successCookie,
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       oneHourAgo,
				AuthID:        testAuthPathSuccess,
				AuthURL:       testControlURL + testAuthPathSuccess,
				Authenticated: false,
			},
		},
		{
			name:   "transition-to-successful-session",
			cookie: successCookie,
			// query "wait" indicates the FE wants to make
			// local api call to wait until session completed.
			query:      "wait=true",
			wantStatus: http.StatusOK,
			wantResp:   &LoginResponse{OK: true},
			wantSession: &browserSession{
				ID:            successCookie,
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       oneHourAgo,
				AuthID:        testAuthPathSuccess,
				AuthURL:       testControlURL + testAuthPathSuccess,
				Authenticated: true,
			},
		},
		{
			name:       "query-existing-complete-session",
			cookie:     successCookie,
			wantStatus: http.StatusOK,
			wantResp:   &LoginResponse{OK: true},
			wantSession: &browserSession{
				ID:            successCookie,
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       oneHourAgo,
				AuthID:        testAuthPathSuccess,
				AuthURL:       testControlURL + testAuthPathSuccess,
				Authenticated: true,
			},
		},
		{
			name:        "transition-to-failed-session",
			cookie:      failureCookie,
			query:       "wait=true",
			wantStatus:  http.StatusUnauthorized,
			wantResp:    nil,
			wantSession: nil, // session deleted
		},
		{
			name:          "failed-session-cleaned-up",
			cookie:        failureCookie,
			wantStatus:    http.StatusOK,
			wantResp:      &LoginResponse{OK: false, AuthURL: testControlURL + testAuthPath},
			wantNewCookie: true,
			wantSession: &browserSession{
				ID:            "GENERATED_ID",
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       timeNow,
				AuthID:        testAuthPath,
				AuthURL:       testControlURL + testAuthPath,
				Authenticated: false,
			},
		},
		{
			name:          "expired-cookie-gets-new-session",
			cookie:        expiredCookie,
			wantStatus:    http.StatusOK,
			wantResp:      &LoginResponse{OK: false, AuthURL: testControlURL + testAuthPath},
			wantNewCookie: true,
			wantSession: &browserSession{
				ID:            "GENERATED_ID",
				SrcNode:       remoteNode.Node.ID,
				SrcUser:       user.ID,
				Created:       timeNow,
				AuthID:        testAuthPath,
				AuthURL:       testControlURL + testAuthPath,
				Authenticated: false,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/auth", nil)
			r.URL.RawQuery = tt.query
			r.RemoteAddr = remoteIP
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tt.cookie})
			w := httptest.NewRecorder()
			s.ServeLogin(w, r)
			res := w.Result()
			defer res.Body.Close()

			// Validate response status/data.
			if gotStatus := res.StatusCode; tt.wantStatus != gotStatus {
				t.Errorf("wrong status; want=%v, got=%v", tt.wantStatus, gotStatus)
			}
			var gotResp *LoginResponse
			if res.StatusCode == http.StatusOK {
				body, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &gotResp); err != nil {
					t.Fatal(err)
				}
			}
			if diff := cmp.Diff(gotResp, tt.wantResp); diff != "" {
				t.Errorf("wrong response; (-got+want):%v", diff)
			}
			// Validate cookie creation.
			sessionID := tt.cookie
			var gotCookie bool
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookieName {
					gotCookie = true
					sessionID = c.Value
					break
				}
			}
			if gotCookie != tt.wantNewCookie {
				t.Errorf("wantNewCookie wrong; want=%v, got=%v", tt.wantNewCookie, gotCookie)
			}
			// Validate browser session contents.
			var gotSesson *browserSession
			if s, ok := s.sessions.Load(sessionID); ok {
				gotSesson = s.(*browserSession)
			}
			if tt.wantSession != nil && tt.wantSession.ID == "GENERATED_ID" {
				// If requested, swap in the generated session ID before
				// comparing got/want.
				tt.wantSession.ID = sessionID
			}
			if diff := cmp.Diff(gotSesson, tt.wantSession); diff != "" {
				t.Errorf("wrong session; (-got+want):%v", diff)
			}
		})
	}
}

var (
	testControlURL      = "http://localhost:8080"
	testAuthPath        = "/a/12345"
	testAuthPathSuccess = "/a/will-succeed"
	testAuthPathError   = "/a/will-error"
)

// mockLocalAPI constructs a test localapi handler that can be used
// to simulate localapi responses without a functioning tailnet.
//
// self accepts a function that resolves to a self node status,
// so that tests may swap out the /localapi/v0/status response
// as desired.
func mockLocalAPI(t *testing.T, whoIs map[string]*apitype.WhoIsResponse, self func() *ipnstate.PeerStatus) *http.Server {
	return &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/localapi/v0/whois":
			addr := r.URL.Query().Get("addr")
			if addr == "" {
				t.Fatalf("/whois call missing \"addr\" query")
			}
			if node := whoIs[addr]; node != nil {
				if err := json.NewEncoder(w).Encode(&node); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				return
			}
			http.Error(w, "not a node", http.StatusUnauthorized)
			return
		case "/localapi/v0/status":
			status := ipnstate.Status{Self: self()}
			if err := json.NewEncoder(w).Encode(status); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			return
		case "/localapi/v0/debug-web-client": // used by TestServeTailscaleAuth
			type reqData struct {
				ID  string
				Src tailcfg.NodeID
			}
			var data reqData
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if data.Src == 0 {
				http.Error(w, "missing Src node", http.StatusBadRequest)
				return
			}
			var resp *tailcfg.WebClientAuthResponse
			if data.ID == "" {
				resp = &tailcfg.WebClientAuthResponse{ID: testAuthPath, URL: testControlURL + testAuthPath}
			} else if data.ID == testAuthPathSuccess {
				resp = &tailcfg.WebClientAuthResponse{Complete: true}
			} else if data.ID == testAuthPathError {
				http.Error(w, "authenticated as wrong user", http.StatusUnauthorized)
				return
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			return
		default:
			t.Fatalf("unhandled localapi test endpoint %q, add to localapi handler func in test", r.URL.Path)
		}
	})}
}
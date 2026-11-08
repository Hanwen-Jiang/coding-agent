package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	pb "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/personalconfig/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestPersonalConfigurationAndAccountSwitchInBrowser(t *testing.T) {
	browser := environmentTestBrowser()
	if browser == "" {
		t.Fatal("Chromium is required for personal configuration browser acceptance")
	}
	ts, _, _, app := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	app.cfg.Workspaces = "runtime" // async wrapper remains in front of the test local workspace implementation
	for _, user := range []string{"alice", "bob"} {
		if err := app.store.UpdateEnvironments("acme", func(all []Environment) ([]Environment, error) {
			return append(all, Environment{ID: "env-" + user, Name: "Environment " + user, CreatedBy: user, Privacy: "private", Version: 1}), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	documents := map[string]map[string]any{}
	for _, user := range []string{"alice", "bob"} {
		documents[user] = map[string]any{"instructions": "instructions " + user, "defaults": map[string]any{"skillNames": []string{"review"}, "environmentId": "env-" + user}}
	}
	app.api.client.Transport = personalTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "PersonalConfigService") && !strings.Contains(r.URL.Path, "SkillService") && !strings.Contains(r.URL.Path, "McpConnectionService") {
			return http.DefaultTransport.RoundTrip(r)
		}
		who, err := app.ingress.Authenticate(r.Context(), ingress.Credentials{BearerToken: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListSkills"):
			return workspaceResponse(200, map[string]any{"skills": []any{map[string]any{"name": "review", "version": "user/v1", "description": "Review changes"}, map[string]any{"name": "test", "version": "user/v1", "description": "Run tests"}}}), nil
		case strings.HasSuffix(r.URL.Path, "/ListMcpConnections"):
			return workspaceResponse(200, map[string]any{"connections": []any{}}), nil
		case strings.HasSuffix(r.URL.Path, "/PutPersonalConfig"):
			// Runtime accepts canonical proto names and responds with the normal
			// Connect camelCase JSON wire format.
			raw, _ := json.Marshal(request["document"])
			doc := &pb.PersonalConfigDocument{}
			if err := protojson.Unmarshal(raw, doc); err != nil {
				return nil, err
			}
			raw, _ = protojson.Marshal(doc)
			var normalized map[string]any
			if err := json.Unmarshal(raw, &normalized); err != nil {
				return nil, err
			}
			documents[who.UserID] = normalized
		}
		return workspaceResponse(200, map[string]any{"config": map[string]any{"version": "user/v2", "digest": "frozen-digest", "document": documents[who.UserID]}}), nil
	})
	args, _ := json.Marshal(map[string]string{"pid": pid})
	result := make(chan string, 1)
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__personal_probe.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, "const C="+string(args)+";\n"+personalBrowserProbe)
		case "/__personal_result":
			data, _ := io.ReadAll(r.Body)
			select {
			case result <- string(data):
			default:
			}
			w.WriteHeader(204)
		case "/":
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, r)
			for k, v := range out.Header() {
				w.Header()[k] = v
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: app.ingress.IssueToken("acme", "alice", time.Hour), Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: "en", Path: "/"})
			_, _ = io.WriteString(w, strings.Replace(out.Body.String(), "</body>", `<script type="module" src="/__personal_probe.js"></script></body>`, 1))
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	profile, err := os.MkdirTemp("", "coding-personal-browser")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check", "--window-size=1440,1000", "--user-data-dir="+profile, server.URL+"/#/w/"+pid+"/new")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	select {
	case got := <-result:
		if got != "ok" {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal("personal configuration browser timeout")
	}
}

const personalBrowserProbe = `
const {t}=await import('/assets/i18n.js');
const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{const end=Date.now()+15000;while(Date.now()<end){let v=fn();if(v)return v;await new Promise(r=>setTimeout(r,30));}throw Error(label+'\n'+document.body.innerText.slice(0,3500));};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const request=async(method,path,body)=>{const r=await fetch(path,{method,headers:{'X-Coding-Web':'1','Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});if(!r.ok)throw Error(await r.text());return r.json();};
try {
 (await wait(()=>$('.task-settings-trigger'),'task settings')).click();
 await wait(()=>$('[data-environment-select]')?.textContent.includes('Environment alice'),'personal default environment');$('[data-task-settings-apply]').click();
 $('.profile').click();$$('.pop-item').find(x=>x.textContent.includes(t('account.title'))).click();
 (await wait(()=>$('.personal-open'),'personal configuration entry')).click();
 const instructions=await wait(()=>$('.personal-instructions'),'personal editor');
 check(instructions.value==='instructions alice','private document missing');
 instructions.value='Prefer concise answers';$('.personal-save').click();
 await wait(()=>!$('dialog.personal-config'),'saved personal package');
 $('dialog.account').close();
 const stored=await request('GET','/api/personal-config');check(stored.config.document.instructions==='Prefer concise answers','save did not reach service');
 const history=await fetch('/api/personal-config?version=user%2Fv2');check(history.headers.get('Cache-Control')==='no-store','personal history was cacheable');
 $('.task-settings-trigger').click();(await wait(()=>$('.personal-tools'),'session tool settings')).click();await wait(()=>$('.personal-skills input'),'session tools');
 check($('.personal-skills input[data-reference="review"]').checked,'default skill not selected');
 check(!$('.personal-skills input[data-reference="test"]').checked,'default subset expanded');
 $$('.personal-skills input,.personal-mcp input').forEach(x=>x.checked=false);$('.personal-save').click();
 await wait(()=>!$('dialog.personal-config'),'session choices closed');
 const input=$('#composer');input.value='Run the test';input.dispatchEvent(new Event('input',{bubbles:true}));$('.send').click();
 await wait(()=>location.hash.includes('/s/'),'async session created');
 const sid=location.hash.split('/s/')[1].split('?')[0];
 let list=await request('GET','/api/sessions');const session=list.find(x=>x.id===sid);
 check(session.personal_config?.skill_names?.length===0&&session.personal_config?.mcp_connection_ids?.length===0,'empty session selection lost');
 (await wait(()=>$('.task-settings-trigger'),'task settings')).click();(await wait(()=>$('.personal-tools'),'session tool button')).click();await wait(()=>$('.personal-skills input'),'session tools reopened');
 check(!$$('.personal-skills input').some(x=>x.checked),'explicit empty reset to defaults');$('dialog.personal-config').close();
 $('.profile').click();$$('.pop-item').find(x=>x.textContent.includes(t('menu.logout'))).click();
 await wait(()=>$('input[autocomplete=username]'),'logout');
 localStorage.setItem('cw_environment','env-alice');
 $('input[autocomplete=organization]').value='acme';$('input[autocomplete=username]').value='bob';$('form').requestSubmit();
 await wait(()=>$('.profile')?.textContent.includes('bob'),'login Bob');
 location.hash='#/w/'+C.pid+'/new';
 await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 (await wait(()=>$('.task-settings-trigger'),'task settings')).click();
 await wait(()=>$('[data-environment-select]')?.textContent.includes('Environment bob'),'account-specific default environment');$('[data-task-settings-apply]').click();
 list=await request('GET','/api/sessions');check(!list.find(x=>x.id===sid).personal_config,'Alice session references leaked to Bob');
 $('.task-settings-trigger').click();(await wait(()=>$('.personal-tools'),'session tool settings')).click();await wait(()=>$('.personal-skills input'),'Bob tool defaults');
 check($('.personal-skills input[data-reference="review"]').checked,'Bob inherited Alice empty override');
 await fetch('/__personal_result',{method:'POST',body:'ok'});
} catch(err){await fetch('/__personal_result',{method:'POST',body:String(err.stack||err)});}
`

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
	"sync/atomic"
	"testing"
	"time"
)

func TestMultiRepositoryUIInBrowser(t *testing.T) {
	browser := environmentTestBrowser()
	if browser == "" {
		t.Skip("Chrome/Edge not found; set CODING_TEST_BROWSER")
	}
	for _, lang := range []string{"zh", "en"} {
		t.Run(lang, func(t *testing.T) {
			ts, _, _, app := newServerWith(t, nil)
			fake := &multiWorkspaceFake{repos: make(map[string][]WorkspaceRepo)}
			fake.files = []DiffStat{{Path: "notes.txt", Added: 1}}
			app.ws = fake
			app.cfg.Workspaces = "runtime" // exercise 202 + browser polling through the real wrapper
			c := newClient(t, ts.URL)
			c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
			_, p, _ := c.do("POST", "/api/projects", map[string]string{"name": "multi", "layout": layoutMulti})
			pid := p["id"].(string)
			_, sess, _ := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "Multi session"})
			sid := sess["id"].(string)
			args, _ := json.Marshal(map[string]string{"pid": pid, "sid": sid})
			probe := "const C = " + string(args) + ";\n" + multiRepositoryUIProbe
			result := make(chan string, 1)
			handler := app.Handler()
			var admissions, polls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Coding-Async") == "1" {
					admissions.Add(1)
				}
				if strings.HasPrefix(r.URL.Path, "/api/workspace-operations/who-") {
					polls.Add(1)
				}
				switch r.URL.Path {
				case "/__probe.js":
					w.Header().Set("Content-Type", "text/javascript")
					_, _ = io.WriteString(w, probe)
				case "/__result":
					data, _ := io.ReadAll(r.Body)
					select {
					case result <- string(data):
					default:
					}
					w.WriteHeader(204)
				case "/":
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					for k, v := range rec.Header() {
						w.Header()[k] = v
					}
					http.SetCookie(w, &http.Cookie{Name: cookieName, Value: app.ingress.IssueToken("acme", "alice", time.Hour), Path: "/", HttpOnly: true})
					http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: "/"})
					_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", `<script type="module" src="/__probe.js"></script></body>`, 1))
				default:
					handler.ServeHTTP(w, r)
				}
			}))
			defer server.Close()
			profile, err := os.MkdirTemp("", "coding-multi-browser")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for range 50 {
					if os.RemoveAll(profile) == nil {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check", "--window-size=1440,1000", "--user-data-dir="+profile, server.URL+"/#/w/"+pid+"/s/"+sid)
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
				t.Fatal("multi-repository browser timeout")
			}
			if fake.commits != 1 || fake.selected != "app" {
				t.Fatalf("selected write repository: commits=%d repo=%q", fake.commits, fake.selected)
			}
			if admissions.Load() < 6 || polls.Load() < 6 {
				t.Fatalf("browser bypassed async operations: admissions=%d polls=%d", admissions.Load(), polls.Load())
			}
			if len(fake.repos[pid]) != 0 {
				t.Fatal("session detach affected baseline")
			}
		})
	}
}

const multiRepositoryUIProbe = `
const {t} = await import('/assets/i18n.js');
const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
const wait=async(test,label)=>{const end=Date.now()+10000;while(Date.now()<end){const v=test();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label+'\n'+document.body.innerText.slice(0,3000));};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const post=async(path,body)=>{const r=await fetch(path,{method:'POST',headers:{'X-Coding-Web':'1','Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw Error(await r.text());return r.json();};
try {
 (await wait(()=>$('[data-topbar-menu]'),'workspace menu')).click();
 (await wait(()=>$('[data-repo-manage]'),'manage repositories')).click();
 await wait(()=>$('dialog [data-repo-attach]'),'attach form');
 const input=key=>$('dialog [aria-label="'+t(key)+'"]');
 const attach=async(name,access)=>{
  input('new.url').value='https://example.com/'+name+'.git';input('repos.name').value=name;input('repos.path').value=name;input('repos.access').value=access;
  $('dialog [data-repo-attach]').click();
  await wait(()=>$('dialog [data-repo-name="'+name+'"]')&&!$('dialog [data-repo-attach]').disabled,'attached '+name);
 };
 await attach('app','REPO_ACCESS_WRITE');await attach('docs','REPO_ACCESS_READ');
 input('new.url').value='https://example.com/broken.git';input('repos.name').value='broken';input('repos.path').value='broken';
 $('dialog [data-repo-attach]').click();
 await wait(()=>$('dialog [role="alert"]').textContent&&!$('dialog [data-repo-attach]').disabled,'attach failure');
 check($$('dialog [data-repo-name]').length===2,'failed attach lost existing repositories');
 $$('dialog button').find(b=>b.textContent===t('common.close')).click();
 const picker=await wait(()=>$('[data-repo-picker]'),'repository selector');
 picker.value='docs';picker.dispatchEvent(new Event('change',{bubbles:true}));
 $('.topbar button[title="'+t('drawer.title')+'"]').click();
 await wait(()=>$('[data-repo-readonly]'),'read-only warning');
 let commit=$('[data-git-commit]');
 check(commit && commit.disabled,'read-only commit must be disabled');
 const writable=$('[data-repo-picker]');writable.value='app';writable.dispatchEvent(new Event('change',{bubbles:true}));
 commit=await wait(()=>{const b=$('[data-git-commit]');return b&&!b.disabled&&b;},'writable commit');
 commit.click();
 await wait(()=>document.body.innerText.includes(t('commit.done',{hash:'abc'})),'scoped commit');
 document.querySelector('.drawer-head button[title="'+t('drawer.close')+'"]').click();
 $('[data-topbar-menu]').click();(await wait(()=>$('[data-repo-manage]'),'manage repositories')).click();await wait(()=>$('dialog [data-repo-name="docs"]'),'repo manager');
 let confirmations=0;window.confirm=()=>{confirmations++;return true;};
 $('dialog [data-repo-name="docs"] button').click();
 await wait(()=>!$('dialog [data-repo-name="docs"]')&&!$('dialog [data-repo-attach]').disabled,'confirmed detach');
 check(confirmations===2,'dirty detach requires a second explicit confirmation');
 check($$('dialog [data-repo-name]').length===1,'detach preserved other repository');
 await fetch('/__result',{method:'POST',body:'ok'});
} catch(err) {await fetch('/__result',{method:'POST',body:String(err.stack||err)});}
`

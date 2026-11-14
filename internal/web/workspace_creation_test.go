package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Exercise the visible picker against the public HTTP contract, including a partial
// failure. The same workspace and successful attachment must survive a retry.
func TestRepositoryMultiSelectAndManagementRoutesInBrowser(t *testing.T) {
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1600,1000"}, {"en", "1600,1000"}, {"zh", "390,844"}, {"en", "390,844"}} {
		language := scenario.language
		t.Run(language+"/"+scenario.viewport, func(t *testing.T) {
			var mu sync.Mutex
			var creations []params
			var attachments []params
			var repos []params
			failed := false
			extra := map[string]any{
				"/api/environments":    []any{},
				"/api/personal-config": params{"config": params{}},
				"/api/git/github/repos": []params{
					{"provider": "github", "full_name": "owner/app", "clone_url": "https://example.com/owner/app.git", "default_branch": "main", "can_push": true},
					{"provider": "github", "full_name": "team/app", "clone_url": "https://example.com/team/app.git", "default_branch": "develop", "can_push": false},
				},
				"/api/skills": params{"available": true, "skills": []params{{"name": "review", "scope": "platform", "description": "Review changes"}}},
				"/api/mcp":    params{"available": true, "connections": []params{{"connection_id": "docs", "name": "docs", "state": "ready", "url": "https://example.com/mcp", "tools": []params{{"tool": "search", "enabled": true}}}}},
				"POST /api/projects": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body params
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					creations = append(creations, body)
					mu.Unlock()
					time.Sleep(180 * time.Millisecond)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(params{"id": "multi", "name": body["name"], "layout": "WORKSPACE_LAYOUT_MULTI"})
				}),
				"GET /api/projects/multi/repos": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(params{"repos": repos})
				}),
				"POST /api/projects/multi/repos": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body params
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					defer mu.Unlock()
					attachments = append(attachments, body)
					repo := body["repo"].(map[string]any)
					w.Header().Set("Content-Type", "application/json")
					if repo["path"] == "api" && !failed {
						failed = true
						w.WriteHeader(409)
						_ = json.NewEncoder(w).Encode(params{"error": "temporary unavailable"})
						return
					}
					repos = append(repos, repo)
					_ = json.NewEncoder(w).Encode(params{"repo": repo})
				}),
			}
			raw := runAccountProbe(t, language, workspaceCreationProbe, params{"providers": []params{{"id": "github", "name": "GitHub", "git": true, "configured": true}}}, extra, scenario.viewport)
			var result struct {
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("browser: %v %s", err, raw)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(creations) != 1 || creations[0]["layout"] != "WORKSPACE_LAYOUT_MULTI" {
				t.Fatalf("one multi workspace expected: %+v", creations)
			}
			if len(attachments) != 3 || len(repos) != 2 {
				t.Fatalf("partial retry duplicated a successful attachment: %+v", attachments)
			}
			if repos[0]["path"] != "app" || repos[0]["access"] != "REPO_ACCESS_WRITE" || repos[0]["primary"] != true {
				t.Fatalf("first repository: %+v", repos[0])
			}
			if repos[1]["path"] != "api" || repos[1]["ref"] != "release" || repos[1]["access"] != "REPO_ACCESS_READ" {
				t.Fatalf("second repository: %+v", repos[1])
			}
		})
	}
}

const workspaceCreationProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));
addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{const end=Date.now()+10000;while(Date.now()<end){const found=fn();if(found)return found;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.innerText.slice(0,1200));};
const check=(value,msg)=>{if(!value)throw Error(msg);};
const {t}=await import('/assets/i18n.js');
try{
 await wait(()=>$('dialog.account'),'account');$('dialog.account').close();
 location.hash='#/workspaces';
 (await wait(()=>$$('.page-head button').find(b=>b.textContent.includes(t('ws.create'))),'create')).click();
 await wait(()=>$$('[data-repository]').length===2,'repository choices');
 check($$('.workspace-source-tabs button').length===3,'multi workspace must not be a separate source');
 check($('dialog.new-workspace').scrollWidth <= $('dialog.new-workspace').clientWidth,'picker overflow');
 $$('[data-repository]')[0].click();$$('[data-repository]')[1].click();
 check($$('[data-selected-repo]').length===2,'both repository selections retained');
 let row=$$('[data-selected-repo]')[1];
 check(row.querySelector('.repo-path').value==='app-2','duplicate repository names need unique directories');
 row.querySelector('.repo-ref').value='release';row.querySelector('.repo-ref').dispatchEvent(new Event('input'));
 row.querySelector('.repo-path').value='api';row.querySelector('.repo-path').dispatchEvent(new Event('input'));
 const search=$('.new-workspace .repo-search'); search.value='owner';search.dispatchEvent(new Event('input'));
 check($$('[data-repository]').length===1 && $$('[data-selected-repo]').length===2,'search must preserve hidden selections');
 $('[data-workspace-create]').click();
 check($('.new-workspace .d-body > .col').inert && $('[data-repository]').disabled,'creation freezes editable controls');
 await wait(()=>$('.new-workspace .err-text')?.textContent.includes('temporary unavailable')&&!$('[data-workspace-create]').disabled,'partial failure');
 check($$('[data-selected-repo]').length===2,'retry preserves selection');
 $('[data-workspace-create]').click();
 await wait(()=>!$('dialog.new-workspace')&&location.hash.includes('/w/multi/new'),'workspace opened');
 location.hash='#/skills';
 await wait(()=>$('section.skills .skill-row'),'skills page');
 check(!$('dialog.skills'),'skills navigation must render a page');
 check(document.documentElement.scrollWidth <= innerWidth,'Skills page overflow');
 check(!$('.side').textContent.includes(t('settings.title')) && !$('.side').textContent.includes(t('mcp.title')),'configuration must live inside Settings');
 check(getComputedStyle($('.management-page .d-body')).maxHeight==='none','management page must not retain dialog height limit');
 location.hash='#/mcp';
 await wait(()=>$('section.mcp .mcp-row'),'MCP page');
 check(!$('dialog.mcp'),'MCP navigation must render a page');
 check(!document.body.innerText.includes('mcp.url'),'all new copy must be translated');
 check(document.documentElement.scrollWidth<=innerWidth,'desktop page overflow '+document.documentElement.scrollWidth+'/'+innerWidth+' '+[...document.querySelectorAll('body *')].filter(el=>el.getBoundingClientRect().right>innerWidth+1).slice(0,12).map(el=>el.className+':'+el.getBoundingClientRect().right).join(';')); 
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

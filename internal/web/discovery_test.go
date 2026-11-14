package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestToolChoiceSummariesAndPublicDiscoveryInBrowser(t *testing.T) {
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"en", "390,844"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			var mu sync.Mutex
			var saved params
			var imports []params
			var mcpQueries []string
			var connections int
			longDescription := strings.Repeat("Review the repository and inspect the existing conventions before preparing a focused commit and pull request. ", 6)
			skills := []params{{"name": "commit-and-pr", "scope": "platform", "description": longDescription}, {"name": "writer", "scope": "user", "description": "Write concise documentation."}, {"name": "review", "scope": "tenant", "description": "Review changes before publishing."}}
			extra := map[string]any{
				"/api/projects":        []params{{"id": "p", "name": "Tool test workspace", "source": "empty"}},
				"/api/personal-config": params{"config": params{"version": "v1", "document": params{"defaults": params{"skillNames": []string{"writer"}}}}},
				"/api/environments":    []any{},
				"GET /api/skills": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					_ = json.NewEncoder(w).Encode(params{"available": true, "skills": skills})
				}),
				"/api/mcp": params{"available": true, "connections": []params{{"connection_id": "docs", "name": "Documentation", "server_name": "Project documentation", "state": "ready", "url": "https://example.com/mcp", "tools": []params{{"tool": "search", "enabled": true}}}}},
				"PUT /api/personal-config": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					_ = json.NewDecoder(r.Body).Decode(&saved)
					_ = json.NewEncoder(w).Encode(params{"config": params{"version": "v2"}})
				}),
				"GET /api/discovery/skills": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("repo") != "anthropics/skills" {
						t.Errorf("unexpected repo %q", r.URL.RawQuery)
					}
					items := []params{{"id": "plain", "name": "writer", "description": "A standalone writing Skill.", "repository": "anthropics/skills", "path": "skills/writer/SKILL.md", "ref": "main", "url": "https://github.com/anthropics/skills"}, {"id": "bundle", "name": "with-files", "description": "Includes required helpers.", "repository": "anthropics/skills", "path": "skills/bundle/SKILL.md", "ref": "main"}}
					next := "next-page"
					if r.URL.Query().Get("cursor") == "next-page" {
						items = []params{{"id": "page2", "name": "another-skill", "repository": "anthropics/skills", "path": "another/SKILL.md", "ref": "main"}}
						next = ""
					}
					_ = json.NewEncoder(w).Encode(params{"items": items, "next_cursor": next, "sources": []params{{"name": "Anthropic", "repository": "anthropics/skills"}}})
				}),
				"GET /api/skills/{name}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(params{"skill": params{"name": r.PathValue("name"), "scope": "platform", "description": longDescription, "instructions": "# Commit guide\n\nReview the diff before committing."}})
				}),
				"GET /api/discovery/skills/detail": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					bundle := strings.Contains(r.URL.Query().Get("path"), "bundle")
					_ = json.NewEncoder(w).Encode(params{"name": "writer", "description": "A standalone writing Skill.", "instructions": "# Writer\n\nUse the project style.", "repository": "anthropics/skills", "url": "https://github.com/anthropics/skills", "importable": !bundle, "has_supporting_files": bundle, "reference_files": []string{"scripts/helper.py"}})
				}),
				"PUT /api/skills/{name}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					var body params
					_ = json.NewDecoder(r.Body).Decode(&body)
					body["name"] = r.PathValue("name")
					imports = append(imports, body)
					skills = append(skills, params{"name": r.PathValue("name"), "scope": "user"})
					_ = json.NewEncoder(w).Encode(params{"skill": body})
				}),
				"GET /api/discovery/mcp": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					mcpQueries = append(mcpQueries, r.URL.Query().Get("query"))
					mu.Unlock()
					if r.URL.Query().Get("query") == "failure" {
						w.WriteHeader(502)
						_ = json.NewEncoder(w).Encode(params{"error": "Registry temporarily unavailable"})
						return
					}
					_ = json.NewEncoder(w).Encode(params{"items": []params{{"id": "remote", "name": "org/remote-server", "description": "Remote server", "repository": "https://github.com/example/remote", "remotes": []params{{"url": "https://example.com/remote", "transport": "streamable-http", "auth_unknown": true}}}, {"id": "local", "name": "org/local", "description": "Local package", "repository": "https://github.com/example/local", "packages": []params{{"identifier": "local-package", "registry_type": "npm", "version": "1.0", "transport": "stdio"}}}}})
				}),
				"POST /api/mcp": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					connections++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(params{})
				}),
			}
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, scenario.language, publicDiscoveryProbe, params{}, extra, scenario.viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("browser: %v %s", err, raw)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(imports) != 1 || imports[0]["name"] != "imported-writer" || imports[0]["instructions"] != "# Writer\n\nUse the project style." {
				t.Fatalf("preview import: %+v", imports)
			}
			if connections != 0 {
				t.Fatal("discovery must not auto-connect")
			}
			for _, q := range mcpQueries {
				if q == "ab" {
					t.Fatal("short registry query reached upstream")
				}
			}
			doc, ok := saved["document"].(map[string]any)
			if !ok {
				t.Fatalf("selection not saved: %+v", saved)
			}
			defaults := doc["defaults"].(map[string]any)
			names := defaults["skillNames"].([]any)
			if len(names) != 3 {
				t.Fatalf("search discarded hidden selections: %v", names)
			}
		})
	}
}

const publicDiscoveryProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<300;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.innerText.slice(0,1200));};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const {t,lang}=await import('/assets/i18n.js');
try {
 (await wait(()=>$('dialog.account'),'account')).close(); location.hash='#/settings/personal';
 const row=await wait(()=>$('[data-tool-choice="commit-and-pr"]'),'tool choices');
 check(row.querySelector('.tool-choice-name').textContent==='commit-and-pr','name must not include description');
 check(row.querySelector('.tool-choice-source').textContent===t('skills.scope.platform'),'scope badge missing');
 $('.personal-skills .tool-choice-actions button:last-child').click();
 check($('.personal-skills .tool-choice-count').textContent===t('personal.inheritAll'),'empty defaults must disclose inheritance');
 $('[data-tool-choice="writer"] [data-reference]').click();
 const short=row.querySelector('.tool-choice-description');
 check(getComputedStyle(short).webkitLineClamp==='2' && short.getBoundingClientRect().height<42,'description must be two lines');
 if(lang==='zh'){
 check(short.textContent===t('skills.summary.commit-and-pr'),'built-in Chinese summary missing');
 const localized=$('.personal-skills .tool-choice-search');localized.value='审阅';localized.dispatchEvent(new Event('input'));
 check($$('.personal-skills [data-tool-choice]').length===1,'visible Chinese summaries must be searchable');
 localized.value='';localized.dispatchEvent(new Event('input'));
 }
 row.querySelector('details summary').click();check(row.querySelector('details').open && row.querySelector('details p').textContent.length>300,'full original description unavailable');
 row.querySelector('input').click();
 const search=$('.personal-skills .tool-choice-search');search.value='publishing';search.dispatchEvent(new Event('input'));
 check($$('.personal-skills [data-tool-choice]').length===1,'search filtering');$('.personal-skills [data-reference]').click();
 check($('.personal-skills .tool-choice-count').textContent===t('personal.selectedTools',{n:3,total:3}),'selection lost during search');
 $('.personal-save').click();await wait(()=>document.body.textContent.includes(t('personal.saved')),'saved choices');
 location.hash='#/settings/skills';
 const owned=await wait(()=>$('section.skills .skill-row[data-name="commit-and-pr"]'),'owned Skill');
 check(getComputedStyle(owned.querySelector('.skill-row-summary')).webkitLineClamp==='2','owned description must be clamped');
 if(lang==='zh')check(owned.querySelector('.skill-row-summary').textContent===t('skills.summary.commit-and-pr'),'owned built-in summary');
 check(!!owned.querySelector('details')&&!owned.querySelector('details').open,'owned full description must be collapsed');
 owned.querySelector('.skill-view').click();await wait(()=>$('.skill-instructions-document')?.textContent.includes('Review the diff before committing.'),'read-only instructions');
 check(!$('dialog.skill-preview textarea'),'read-only Skill must not show editor');$('dialog.skill-preview .d-foot button').click();
 $('section.skills [data-discovery-tab="explore"]').click();
 await wait(()=>$('[data-discovery-id="plain"]'),'live discovery results');
 if(innerWidth>700)check(Math.abs($('.discovery-source-row').getBoundingClientRect().top-$('.discovery-search-row').getBoundingClientRect().top)<2,'desktop discovery toolbar must be one row');
 $('.discovery-more').click();await wait(()=>$('[data-discovery-id="page2"]'),'pagination');
 $('[data-discovery-id="bundle"] [data-discovery-preview]').click();await wait(()=>$('dialog.discovery-preview .discovery-supporting'),'supporting files');
 check(!$('dialog.discovery-preview [data-discovery-add]'),'bundle must not silently import without files');$('dialog.discovery-preview .d-foot button').click();
 $('[data-discovery-id="plain"] [data-discovery-preview]').click();(await wait(()=>$('[data-discovery-add]'),'add preview')).click();
 await wait(()=>$('dialog.discovery-preview .err-text')?.textContent===t('explore.nameExists'),'name collision guard');
 $('.discovery-import-name').value='imported-writer';$('[data-discovery-add]').click();await wait(()=>!$('dialog.discovery-preview'),'import finished');
 check(!$('section.skills .d-body').hidden,'import returns to library');
 location.hash='#/settings/mcp';(await wait(()=>$('section.mcp [data-discovery-tab="explore"]'),'MCP Explore')).click();
 await wait(()=>$('[data-discovery-id="local"]'),'MCP results');
 let query=$('.discovery-query');query.value='ab';query.closest('form').requestSubmit();check($('.discovery-notice').textContent===t('explore.minQuery'),'short query feedback');
 $('[data-discovery-id="local"] [data-discovery-preview]').click();await wait(()=>$('.discovery-package'),'local package details');
 check(!$('[data-discovery-connect]') && $('.discovery-package').textContent.includes('local-package'),'stdio must not show Connect');$('dialog.discovery-preview .d-foot button').click();
 $('[data-discovery-id="remote"] [data-discovery-preview]').click();(await wait(()=>$('[data-discovery-connect]'),'remote endpoint')).click();
 await wait(()=>!$('dialog.discovery-preview')&&!$('section.mcp .d-body').hidden,'remote form');
 check($('.mcp-url').value==='https://example.com/remote' && $('.mcp-name').value==='remote_server','remote endpoint must prefill only');
 $('section.mcp [data-discovery-tab="explore"]').click();query=$('.discovery-query');query.value='failure';query.closest('form').requestSubmit();
 await wait(()=>$('.discovery-notice').textContent.includes('Registry temporarily unavailable'),'upstream error');
 check(!!$('.discovery-notice button'),'upstream error has retry');
 check(document.documentElement.scrollWidth<=innerWidth,'settings overflow');
 location.hash='#/w/p/new';(await wait(()=>$('.task-settings-trigger'),'task settings')).click();(await wait(()=>$('.personal-tools'),'session tools')).click();
 await wait(()=>$('dialog.personal-config .tool-choice-list'),'session choices');
 $('dialog.personal-config .personal-skills .tool-choice-actions button:last-child').click();
 check($('dialog.personal-config .personal-skills .tool-choice-count').textContent===t('personal.selectedTools',{n:0,total:4}),'session empty choice must not inherit');
 $('dialog.personal-config .personal-save').click();await wait(()=>!$('dialog.personal-config'),'save session tools');
 $('.task-settings-trigger').click();(await wait(()=>$('.personal-tools'),'session tools')).click();await wait(()=>$('dialog.personal-config .tool-choice-list'),'reopened session choices');
 check($$('dialog.personal-config .personal-skills input:checked').length===0,'explicit session none must persist');
} catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

func TestWorkspaceSourceTabsKeepTheirHeightInShortWindows(t *testing.T) {
	var repos []params
	for i := range 30 {
		repos = append(repos, params{"provider": "github", "full_name": "owner/repo" + string(rune('A'+i)), "clone_url": "https://example.com/repo" + string(rune('A'+i)) + ".git", "default_branch": "main", "can_push": true})
	}
	conns := []params{{"provider": "github", "git": true}, {"provider": "gitlab", "git": true}, {"provider": "gitee", "git": true}, {"provider": "bitbucket", "git": true}}
	extra := map[string]any{"/api/me": params{"tenant": "preview", "user": "test", "connections": conns}, "/api/git/github/repos": repos, "/api/samples": []params{{"id": "sample", "name": "Sample project", "description": "A sample repository"}}, "/api/personal-config": params{"config": params{}}, "/api/environments": []any{}}
	for _, viewport := range []string{"900,600", "390,644"} {
		t.Run(viewport, func(t *testing.T) {
			var out struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, "en", sourceTabsHeightProbe, params{}, extra, viewport)
			if err := json.Unmarshal(raw, &out); err != nil || len(out.Errors) > 0 {
				t.Fatalf("browser: %v %s", err, raw)
			}
		})
	}
}

const sourceTabsHeightProbe = `
const out={errors:[]};const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn)=>{for(let i=0;i<300;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,20));}throw Error('timeout');};
const check=(v,m)=>{if(!v)throw Error(m);};const {t}=await import('/assets/i18n.js');
try{
 (await wait(()=>$('dialog.account'))).close();location.hash='#/workspaces';
 for(const theme of ['light','dark']){
  document.documentElement.dataset.theme=theme;
  (await wait(()=>$$('.page-head button').find(b=>b.textContent.includes(t('ws.create'))))).click();
  await wait(()=>$$('[data-repository]').length===30);
  for(let i=0;i<12;i++)$$('[data-repository]')[i].click();
  const assertTabs=()=>{const body=$('.new-workspace .d-body'),tabs=$('.workspace-source-tabs');body.scrollTop=0;const r=tabs.getBoundingClientRect(),b=body.getBoundingClientRect();check(r.height>=36,'collapsed tabs '+theme+' '+r.height);check(r.top>=b.top && r.bottom<=b.bottom,'tabs not initially visible');check(body.scrollHeight>body.clientHeight,'long form needs body scrolling');};
  assertTabs();$$('.workspace-source-tabs button')[1].click();$$('.workspace-source-tabs button')[0].click();assertTabs();
  check($('dialog.new-workspace').scrollWidth<=$('dialog.new-workspace').clientWidth,'dialog horizontal overflow');
  $('dialog.new-workspace').close();await wait(()=>!$('dialog.new-workspace'));
 }
}catch(e){out.errors.push(String(e.stack||e));}await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The browser uses the production frontend against real multipart and SSE
// endpoints. Only persistence and task execution are replaced by this fixture.
func sourceBrowserAPI(initialSources ...params) map[string]any {
	var mu sync.Mutex
	sources := map[string][]params{}
	sources["/api/projects/p/sessions/s"] = append([]params{}, initialSources...)
	tasks := map[string][]params{}
	events := map[string][]params{}
	attempts := map[string]int{}
	requests := []params{}
	updates := make(chan params, 16)
	nextSource := 0
	var previewPNG []byte
	extra := map[string]any{
		"/api/projects": []params{{"id": "p", "name": "Sources", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Source upload"}},
		"/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
		"POST /api/projects/p/sessions": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests = append(requests, params{"action": "create-session"})
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(params{"id": "created", "project_id": "p", "title": "New upload", "task_settings": params{"sandbox": "write_reversible"}})
		}),
		"GET /__observed": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(params{"requests": requests, "attempts": attempts})
		}),
		"GET /__preview.png": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			data := append([]byte{}, previewPNG...)
			mu.Unlock()
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		}),
		"POST /__push": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var update struct {
				Type  string `json:"type"`
				Value params `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&update)
			base := "/api/projects/p/sessions/s"
			mu.Lock()
			event := snapshotEvent(len(events[base])+1, "submitted", update.Type, update.Value)
			events[base] = append(events[base], event)
			mu.Unlock()
			updates <- event
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	for _, session := range []string{"s", "created"} {
		base := "/api/projects/p/sessions/" + session
		extra[base+"/env"] = params{}
		extra[base+"/repos"] = params{"repos": []any{}}
		extra[base+"/changes"] = params{"files": []any{}}
		extra[base+"/settings"] = params{"sandbox": "write_reversible"}
		extra["GET "+base+"/sources"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, params{"action": "list-sources", "base": base})
			list := sources[base]
			if list == nil {
				list = []params{}
			}
			_ = json.NewEncoder(w).Encode(params{"sources": list})
		})
		extra["POST "+base+"/sources"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(12 << 20); err != nil {
				http.Error(w, "expected multipart upload", http.StatusBadRequest)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "file field missing", http.StatusBadRequest)
				return
			}
			defer file.Close()
			content, _ := io.ReadAll(file)
			mu.Lock()
			defer mu.Unlock()
			attempts[header.Filename]++
			requests = append(requests, params{"action": "upload", "base": base, "name": header.Filename, "bytes": len(content), "multipart": strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary="), "attempt": attempts[header.Filename]})
			if header.Filename == "fail-once.txt" && attempts[header.Filename] == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(params{"error": "Upload interrupted. Retry this file."})
				return
			}
			nextSource++
			source := params{"id": fmt.Sprintf("source-%d", nextSource), "name": header.Filename, "size_bytes": len(content), "mime_type": header.Header.Get("Content-Type"), "url": "/download/" + header.Filename}
			if source["mime_type"] == "image/png" {
				source["preview_url"] = "/__preview.png"
				previewPNG = append([]byte{}, content...)
			}
			sources[base] = append(sources[base], source)
			_ = json.NewEncoder(w).Encode(params{"source": source})
		})
		extra["GET "+base+"/tasks"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			list := tasks[base]
			if list == nil {
				list = []params{}
			}
			_ = json.NewEncoder(w).Encode(list)
		})
		extra["POST "+base+"/tasks"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input params
			_ = json.NewDecoder(r.Body).Decode(&input)
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, params{"action": "submit-task", "base": base, "input": input})
			task := params{"id": "submitted", "status": "TASK_STATUS_RUNNING", "message": input["message"], "source_ids": input["source_ids"], "created_at": "2026-11-09T08:00:00Z"}
			tasks[base] = []params{task}
			events[base] = []params{snapshotEvent(1, "submitted", "task_accepted", params{}), snapshotEvent(2, "submitted", "round_started", params{"round_seq": 1})}
			_ = json.NewEncoder(w).Encode(task)
		})
		extra["GET "+base+"/snapshot"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			list := []params{}
			if len(tasks[base]) > 0 {
				list = append(list, params{"task_id": "submitted", "status": "running", "last_event_id": len(events[base]), "source": "events", "events": events[base]})
			}
			_ = json.NewEncoder(w).Encode(params{"tasks": list})
		})
		extra["GET "+base+"/tasks/submitted"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(params{"task": tasks[base][0], "runtime": params{}})
		})
		extra[base+"/tasks/submitted/artifacts"] = params{"artifacts": []any{}}
		extra["GET "+base+"/tasks/submitted/events"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			mu.Lock()
			initial := append([]params{}, events[base]...)
			mu.Unlock()
			write := func(event params) {
				data, _ := json.Marshal(event)
				name := strings.ToLower(strings.TrimPrefix(event["type"].(string), "TASK_EVENT_TYPE_"))
				fmt.Fprintf(w, "event: %s\nid: %v\ndata: %s\n\n", name, event["event_id"], data)
				w.(http.Flusher).Flush()
			}
			for _, event := range initial {
				write(event)
			}
			for {
				select {
				case event := <-updates:
					write(event)
				case <-r.Context().Done():
					return
				}
			}
		})
	}
	return extra
}

func TestSourceUploadsReplayAndSubagentCountsInBrowser(t *testing.T) {
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"en", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			checkSourceBrowserResult(t, runAccountProbe(t, scenario.language, sourceBrowserHelpers+sourcesBrowserProbe, params{}, sourceBrowserAPI(), scenario.viewport))
		})
	}
}

func TestNewDraftCreatesSessionBeforeUploadingSources(t *testing.T) {
	checkSourceBrowserResult(t, runAccountProbe(t, "en", sourceBrowserHelpers+draftSourcesBrowserProbe, params{}, sourceBrowserAPI(), "390,744"))
}

func TestExistingSourceCanBeReusedWithoutUploadingAgain(t *testing.T) {
	source := params{"id": "existing-source", "name": "existing-brief.pdf", "mime_type": "application/pdf", "size_bytes": 8192, "url": "/download/existing-brief.pdf"}
	checkSourceBrowserResult(t, runAccountProbe(t, "en", sourceBrowserHelpers+reuseSourceBrowserProbe, params{}, sourceBrowserAPI(source), "1440,900"))
}

func TestAdvertisedSourceCapacityAndLegacyAliasesInBrowser(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	source := params{"id": "canonical-source", "alias_ids": []string{"legacy-source"}, "name": "historical-brief.txt", "mime_type": "text/plain", "size_bytes": 24, "url": "/download/historical-brief.txt"}
	extra := sourceBrowserAPI(source)
	capacity := params{"max_file_bytes": 50 << 20, "max_task_files": 10, "max_task_bytes": 200 << 20, "max_workspace_files": 500, "max_workspace_bytes": int64(2 << 30)}
	extra[base+"/sources/capabilities"] = capacity
	extra["/api/projects/p/sources/capabilities"] = capacity
	task := params{"id": "history-alias", "status": "TASK_STATUS_DONE", "message": "Previously attached source", "source_ids": []string{"legacy-source"}, "created_at": "2026-11-09T08:00:00Z"}
	extra["GET "+base+"/tasks"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode([]params{task}) })
	extra[base+"/tasks/history-alias"] = params{"task": task, "runtime": params{}}
	extra[base+"/tasks/history-alias/artifacts"] = params{"artifacts": []any{}}
	extra["GET "+base+"/snapshot"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(params{"tasks": []params{{"task_id": "history-alias", "status": "done", "last_event_id": 1, "source": "events", "events": []params{snapshotEvent(1, "history-alias", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "Historical source checked."}}})}}}})
	})
	checkSourceBrowserResult(t, runAccountProbe(t, "en", sourceBrowserHelpers+sourceCapacityBrowserProbe, params{}, extra, "390,744"))
}

func TestLateSourceListDoesNotEraseAnUploadedSource(t *testing.T) {
	extra := sourceBrowserAPI()
	release := make(chan struct{})
	var releaseOnce sync.Once
	extra["GET /api/projects/p/sessions/s/sources"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_ = json.NewEncoder(w).Encode(params{"sources": []any{}})
		case <-r.Context().Done():
		}
	})
	extra["POST /__release-source-list"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releaseOnce.Do(func() { close(release) })
		w.WriteHeader(http.StatusNoContent)
	})
	checkSourceBrowserResult(t, runAccountProbe(t, "en", sourceBrowserHelpers+lateSourceListProbe, params{}, extra, "1440,900"))
}

type sourceBrowserIdentity struct {
	mu    sync.Mutex
	value params
}

func (identity *sourceBrowserIdentity) MarshalJSON() ([]byte, error) {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	return json.Marshal(identity.value)
}

func TestExpiredUploadStopsDraftBatchAndDoesNotLeakToNextAccount(t *testing.T) {
	extra := sourceBrowserAPI()
	identity := &sourceBrowserIdentity{value: params{"tenant": "t1", "user": "u1", "connections": []any{}}}
	extra["/api/me"] = identity
	release := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	var uploads []string
	extra["POST /api/projects/p/sessions/created/sources"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "multipart required", http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file required", http.StatusBadRequest)
			return
		}
		_, _ = io.Copy(io.Discard, file)
		_ = file.Close()
		mu.Lock()
		uploads = append(uploads, header.Filename)
		mu.Unlock()
		select {
		case <-release:
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(params{"error": "Session expired"})
		case <-r.Context().Done():
		}
	})
	extra["POST /__expire-upload"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releaseOnce.Do(func() { close(release) })
		w.WriteHeader(http.StatusNoContent)
	})
	extra["GET /__upload-race-state"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(params{"uploads": uploads})
	})
	extra["POST /api/login"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input params
		_ = json.NewDecoder(r.Body).Decode(&input)
		identity.mu.Lock()
		identity.value = params{"tenant": input["tenant"], "user": input["user"], "connections": []any{}}
		identity.mu.Unlock()
		_ = json.NewEncoder(w).Encode(identity)
	})
	checkSourceBrowserResult(t, runAccountProbe(t, "en", sourceBrowserHelpers+expiredSourceBatchProbe, params{"dev_login": true}, extra, "1440,900"))
}

func checkSourceBrowserResult(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var result struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
		t.Fatalf("sources browser: %v %s", err, raw)
	}
}

const sourceBrowserHelpers = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');policy.httpEquiv='Content-Security-Policy';policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob: https:; connect-src 'self'";document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await sleep(25);}throw Error(label+' '+document.body.textContent.slice(-700));};
const check=(value,label)=>{if(!value)throw Error(label);};
const observed=()=>fetch('/__observed').then(response=>response.json());
const pick=files=>{const data=new DataTransfer();for(const file of files)data.items.add(file);const input=$('.composer-file-input');input.files=data.files;input.dispatchEvent(new Event('change',{bubbles:true}));};
const entries=()=>$$('.composer-attachments [data-upload-state]');
const removeAll=async()=>{for(const button of $$('.upload-remove'))button.click();await wait(()=>!entries().length,'remove staged files');};
const geometry=()=>{const shell=$('.prompt-input__shell').getBoundingClientRect(),toolbar=$('.composer-toolbar');check(toolbar.scrollWidth<=toolbar.clientWidth+1,'attachment button overflows footer');check($('.main').scrollWidth<=$('.main').clientWidth+1,'attachment UI overflows main');const attach=$('.composer-attach').getBoundingClientRect();check(attach.left>=shell.left&&attach.right<=shell.right,'attach action outside input surface');};
const count=kind=>Number($('.context-agent-counts [data-agent-count="'+kind+'"] strong')?.textContent||0);
const push=(type,value)=>fetch('/__push',{method:'POST',body:JSON.stringify({type,value})});
`

const sourcesBrowserProbe = `
const reloaded=sessionStorage.getItem('sources-reloaded')==='yes';
try {
 if(!reloaded){(await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';}
 await wait(()=>$('[data-react-composer="true"] .composer-file-input'),'file input');
 const statusToggle=await wait(()=>$('.topbar-actions [aria-pressed]'),'status panel control');if(statusToggle.getAttribute('aria-pressed')==='false')statusToggle.click();
 await wait(()=>$('.context-panel'),'context panel');
 if(reloaded) {
  await wait(()=>$$('.task-source-link').length===2&&$('.context-source-count')?.textContent==='13','persisted sources restored');
  await wait(()=>count('completed')===1&&count('failed')===1,'persisted agent counts restored');
  check(count('running')===0,'finished child counted as running after reload');
  const log=await observed();check(log.requests.filter(item=>item.action==='list-sources').length>=2,'reload did not fetch source list');
  sessionStorage.removeItem('sources-reloaded');geometry();
 } else {
  await wait(()=>!$('.composer-attach').disabled,'attachment allowed');geometry();
  const pasteData=new DataTransfer();pasteData.setData('text/plain','ordinary pasted text');
  const textPaste=new ClipboardEvent('paste',{clipboardData:pasteData,bubbles:true,cancelable:true});$('#composer').dispatchEvent(textPaste);
  check(!textPaste.defaultPrevented,'ordinary text paste was swallowed');
  pick([new File([new Uint8Array(10*1024*1024+1)],'too-large.txt',{type:'text/plain'})]);await sleep(80);
  check(entries().length===0&&(await observed()).requests.filter(item=>item.action==='upload').length===0,'oversized file reached upload');
  pick(Array.from({length:11},(_,i)=>new File(['source '+i],'limit-'+i+'.txt',{type:'text/plain'})));
  await wait(()=>entries().length===10&&entries().every(item=>item.dataset.uploadState==='ready'),'ten-file limit');
  check((await observed()).requests.filter(item=>item.action==='upload').length===10,'more than ten files uploaded');geometry();
  await removeAll();
  pick([new File(['retry me'],'fail-once.txt',{type:'text/plain'})]);
  await wait(()=>$('.upload-retry')&&entries()[0].dataset.uploadState==='error','upload failure retained');
  check($('.send').disabled,'failed upload permits submit');$('.upload-retry').click();
  await wait(()=>entries()[0]?.dataset.uploadState==='ready','failed upload retry');
  check((await observed()).attempts['fail-once.txt']===2,'retry did not target same file');await removeAll();
  pick([new File(['Read this source.'],'notes.txt',{type:'text/plain'})]);
  const canvas=document.createElement('canvas');canvas.width=500;canvas.height=400;
  const context=canvas.getContext('2d');context.fillStyle='#416788';context.fillRect(0,0,500,400);context.fillStyle='#ffffff';context.fillRect(40,40,420,320);
  const png=await new Promise(resolve=>canvas.toBlob(resolve,'image/png'));check(png,'PNG fixture was not generated');
  const data=new DataTransfer();data.items.add(new File([png],'reference.png',{type:'image/png'}));
  const imagePaste=new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true});$('#composer').dispatchEvent(imagePaste);
  check(imagePaste.defaultPrevented,'image paste was not handled as a file');
  await wait(()=>entries().length===2&&entries().every(item=>item.dataset.uploadState==='ready'),'selected sources uploaded');
  const preview=await wait(()=>$('.composer-attachment-preview')?.naturalWidth===500&&$('.composer-attachment-preview'),'full-size local image preview');geometry();
  const bounds=preview.getBoundingClientRect(),chip=preview.closest('.composer-attachment').getBoundingClientRect(),info=preview.closest('.chat-attachment').querySelector('.chat-attachment__info').getBoundingClientRect();
  check(Math.abs(bounds.width-32)<1&&Math.abs(bounds.height-32)<1,'large image did not stay in a 32px preview: '+bounds.width+'x'+bounds.height);
  check(info.width>0&&info.left>=bounds.right-1&&info.right<=chip.right+1,'large preview covers filename or removes its width');
  check(bounds.left>=chip.left&&bounds.right<=chip.right&&bounds.top>=chip.top&&bounds.bottom<=chip.bottom,'large preview escapes its attachment chip');
  check(chip.bottom<=$('.c-row').getBoundingClientRect().top+1,'attachment overlaps editor text');
  await wait(()=>$('.context-source-count')?.textContent==='13','source panel count');
  check($$('.context-source-item').length===3,'source preview must remain compact');$('.context-sources-more').click();
  await wait(()=>$$('.context-source-item').length===13,'view all sources');
  const sourcePreview=await wait(()=>$('.context-source-preview img')?.naturalWidth===500&&$('.context-source-preview img'),'full-size safe server preview');
  check(sourcePreview.closest('.context-source-file').getBoundingClientRect().height<=52,'source file row grows with image size');
  const log=await observed();check(log.requests.filter(item=>item.action==='upload').every(item=>item.multipart&&item.bytes>0),'upload did not send real multipart bytes');
  const sourceIDs=$$('.context-source-item').filter(item=>/notes\.txt|reference\.png/.test(item.textContent)).map(item=>item.dataset.sourceId).sort();
  $('#composer').value='Use the attached sources.';$('#composer').dispatchEvent(new Event('input',{bubbles:true}));await wait(()=>!$('.send').disabled,'send with sources');$('.send').click();
  await wait(()=>$('#turn-submitted')&&$$('.task-source-link').length===2,'task message source links');
  await wait(()=>!entries().length&&$('.composer-attach').disabled,'submitted attachments cleared and running locked');
  const sent=(await observed()).requests.find(item=>item.action==='submit-task');check(JSON.stringify(sent.input.source_ids.sort())===JSON.stringify(sourceIDs),'task did not send exactly selected server source ids');
  const before=(await observed()).requests.filter(item=>item.action==='upload').length;pick([new File(['later'],'while-running.txt')]);await sleep(80);
  check((await observed()).requests.filter(item=>item.action==='upload').length===before,'running task accepted an unsupported attachment');
  await push('node_started',{round_seq:1,node_id:'alpha',mode:'sub_agent'});await push('node_started',{round_seq:1,node_id:'beta',mode:'sub_agent'});
  await wait(()=>count('running')===2&&count('completed')===0,'running agents');
  await push('node_finished',{round_seq:1,node_id:'alpha',status:'succeeded'});await push('node_finished',{round_seq:1,node_id:'beta',status:'failed'});
  await wait(()=>count('running')===0&&count('completed')===1&&count('failed')===1,'agent terminal counts');
  check($('.context-source-add').disabled,'right panel add ignores running read-only state');geometry();
  sessionStorage.setItem('sources-reloaded','yes');location.reload();
 }
}catch(error){out.errors.push(String(error.stack||error));}
if(reloaded||out.errors.length)await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const draftSourcesBrowserProbe = `
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/new';
 await wait(()=>$('[data-react-composer="true"] .composer-file-input')&&!$('.composer-attach').disabled,'draft composer');
 pick([new File(['Deferred local draft.'],'draft.txt',{type:'text/plain'})]);
 await wait(()=>entries().length===1&&entries()[0].dataset.uploadState==='pending','local draft staged');
 check(!(await observed()).requests.some(item=>['upload','create-session','submit-task'].includes(item.action)),'draft uploaded before session creation');
 geometry();await wait(()=>!$('.send').disabled,'file-only task enabled');$('.send').click();
 await wait(()=>location.hash.includes('/s/created')&&$('#turn-submitted')&&$$('.task-source-link').length===1,'draft source delivered to task');
 const actions=(await observed()).requests.filter(item=>['upload','create-session','submit-task'].includes(item.action));
 check(JSON.stringify(actions.map(item=>item.action))===JSON.stringify(['create-session','upload','submit-task']),'draft request order or duplicate upload');
 check(actions[1].base.endsWith('/created')&&actions[2].input.source_ids.length===1&&actions[2].input.message.length>0,'draft task used wrong session or omitted sources');
 check(!entries().length,'draft attachment did not clear');geometry();
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const reuseSourceBrowserProbe = `
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('[data-react-composer="true"] .composer-file-input'),'composer');
 const toggle=await wait(()=>$('.topbar-actions [aria-pressed]'),'status panel control');if(toggle.getAttribute('aria-pressed')==='false')toggle.click();
 const source=await wait(()=>$('.context-source-item[data-source-id="existing-source"]'),'persisted source');
 const use=source.querySelector('.context-source-use');check(use&&!use.disabled,'existing source has no reuse action');
 use.click();await wait(()=>entries().length===1&&entries()[0].dataset.uploadState==='ready','existing source staged');
 check(entries()[0].textContent.includes('existing-brief.pdf'),'reused source metadata missing');
 source.querySelector('.context-source-use').click();await sleep(40);check(entries().length===1,'same source staged twice');
 check(!(await observed()).requests.some(item=>item.action==='upload'),'reuse uploaded file bytes');
 await removeAll();check(!!$('.context-source-item[data-source-id="existing-source"]'),'removing a draft source deleted the stored source');
 check(!(await observed()).requests.some(item=>item.action==='upload'),'removing a reused source uploaded file bytes');
 $('.context-source-item[data-source-id="existing-source"] .context-source-use').click();
 await wait(()=>entries().length===1&&!$('.send').disabled,'reused source ready to submit');
 $('#composer').value='Use the existing brief.';$('#composer').dispatchEvent(new Event('input',{bubbles:true}));$('.send').click();
 await wait(()=>$('#turn-submitted .task-source-link')?.textContent.includes('existing-brief.pdf'),'reused source attached to submitted message');
 const log=await observed(),submitted=log.requests.filter(item=>item.action==='submit-task');
 check(submitted.length===1&&JSON.stringify(submitted[0].input.source_ids)===JSON.stringify(['existing-source']),'task did not preserve the existing server-issued source ID');
 check(!log.requests.some(item=>item.action==='upload'),'submission reuploaded an existing source');
 await wait(()=>!entries().length&&$('.context-source-use').disabled,'reuse draft cleared and running action locked');geometry();
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const sourceCapacityBrowserProbe = `
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('[data-react-composer="true"] .composer-file-input'),'composer');
 await wait(()=>$('.composer-attach')?.getAttribute('aria-label')?.includes('50'),'advertised session capacity');
 const restored=await wait(()=>$('#turn-history-alias .task-source-link'),'historical source alias restored');
 check(restored.textContent.includes('historical-brief.txt'),'old task source id did not resolve through alias_ids');
 pick([new File([new Uint8Array(11*1024*1024)],'eleven-mib.bin',{type:'application/octet-stream'})]);
 await wait(()=>entries().length===1&&entries()[0].dataset.uploadState==='ready','11MiB allowed by advertised capacity');
 const uploaded=(await observed()).requests.filter(item=>item.action==='upload');
 check(uploaded.length===1&&uploaded[0].name==='eleven-mib.bin'&&uploaded[0].bytes===11*1024*1024&&uploaded[0].multipart,'11MiB file did not traverse real multipart upload');
 await removeAll();
 pick([new File([new Uint8Array(50*1024*1024+1)],'above-fifty.bin',{type:'application/octet-stream'})]);await sleep(100);
 check(entries().length===0&&(await observed()).requests.filter(item=>item.action==='upload').length===1,'50MiB+1 exceeded advertised per-file cap before upload');
 geometry();location.hash='#/w/p/new';
 await wait(()=>$('.main-landing')&&$('.composer-attach')?.getAttribute('aria-label')?.includes('50'),'advertised project draft capacity');
 const shared=new Blob([new Uint8Array(50*1024*1024)],{type:'application/octet-stream'});
 pick(Array.from({length:5},(_,index)=>new File([shared],'fifty-'+index+'.bin',{type:'application/octet-stream'})));
 await wait(()=>entries().length===4&&entries().every(item=>item.dataset.uploadState==='pending'),'200MiB boundary staged exactly four 50MiB files');
 await sleep(100);check(!entries().some(item=>item.textContent.includes('fifty-4.bin')),'fifth file bypassed 200MiB task total');
 const after=await observed();check(after.requests.filter(item=>item.action==='upload').length===1&&!after.requests.some(item=>item.action==='create-session'||item.action==='submit-task'),'rejected aggregate or unsubmitted draft sent files upstream');
 geometry();await removeAll();
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const lateSourceListProbe = `
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('[data-react-composer="true"] .composer-file-input')&&!$('.composer-attach').disabled,'upload-enabled composer');
 const toggle=await wait(()=>$('.topbar-actions [aria-pressed]'),'status toggle');if(toggle.getAttribute('aria-pressed')==='false')toggle.click();
 await wait(()=>$('.context-sources[aria-busy="true"]'),'initial source list is still loading');
 pick([new File(['Keep the newly uploaded source.'],'new-before-list.txt',{type:'text/plain'})]);
 await wait(()=>entries().length===1&&entries()[0].dataset.uploadState==='ready','upload completed before source GET');
 await wait(()=>$('.context-source-item')?.textContent.includes('new-before-list.txt'),'new source appears immediately');
 const id=$('.context-source-item').dataset.sourceId;
 await fetch('/__release-source-list',{method:'POST'});
 await wait(()=>$('.context-sources[aria-busy="false"]'),'late empty source list completed');
 check($('.context-source-item')?.dataset.sourceId===id&&$('.context-source-count')?.textContent==='1','late initial GET erased a newer uploaded source');
 await removeAll();$('.context-source-use').click();await wait(()=>entries()[0]?.dataset.uploadState==='ready','source still reusable after late GET');
 check((await observed()).requests.filter(item=>item.action==='upload').length===1,'late GET caused duplicate upload');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const expiredSourceBatchProbe = `
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/new';
 await wait(()=>$('[data-react-composer="true"] .composer-file-input')&&!$('.composer-attach').disabled,'draft composer');
 pick([new File(['old account first'],'private-first.txt',{type:'text/plain'}),new File(['old account second'],'private-second.txt',{type:'text/plain'})]);
 await wait(()=>entries().length===2&&entries().every(item=>item.dataset.uploadState==='pending'),'two local draft sources');
 $('#composer').value='Do not restore this old account draft';$('#composer').dispatchEvent(new Event('input',{bubbles:true}));await wait(()=>!$('.send').disabled,'submit draft batch');$('.send').click();
 await wait(()=>entries().some(item=>item.dataset.uploadState==='uploading'),'first upload in progress');
 let active;
 for(let i=0;i<100;i++){active=await fetch('/__upload-race-state').then(response=>response.json());if(active.uploads?.length)break;await sleep(20);}
 check(JSON.stringify(active.uploads)===JSON.stringify(['private-first.txt']),'batch did not begin with exactly the first upload');
 await fetch('/__expire-upload',{method:'POST'});
 await wait(()=>$('.login-page form'),'401 must return to login');
 $('.login-page input[autocomplete="organization"]').value='t2';$('.login-page input[autocomplete="username"]').value='u2';$('.login-page form').requestSubmit();
 await wait(()=>$('[data-react-composer="true"] .composer-file-input')&&!$('.login-page'),'new account composer');
 await sleep(150);
 const me=await fetch('/api/me').then(response=>response.json());check(me.tenant==='t2'&&me.user==='u2','test did not switch accounts');
 check(entries().length===0&&$('#composer').value===''&&!document.body.textContent.includes('private-first.txt')&&!document.body.textContent.includes('private-second.txt'),'old account draft or source leaked after login');
 check(!$('.composer-attach').disabled,'old submission left new account composer busy');
 const result=await fetch('/__upload-race-state').then(response=>response.json());check(JSON.stringify(result.uploads)===JSON.stringify(['private-first.txt']),'401 did not stop the second file upload');
 check(!(await observed()).requests.some(item=>item.action==='submit-task'),'expired batch still submitted a task');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

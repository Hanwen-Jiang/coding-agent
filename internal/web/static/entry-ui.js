// Entry-point management shares the app's DOM helpers, authentication and design tokens.
// Secrets live only in the one-time dialog; they never enter browser storage or cached connections.
// ask(message, danger) confirms an action; the controller supplies its dialog, and the browser's confirm is the default.
// show(dialog) opens a built <dialog>; the controller hosts it in its Modal, and showModal() is the default.
export function createEntryUI({api, h, t, lang, icon, copy, state, openSession, changed, ask = (message) => Promise.resolve(confirm(message)), show = (d) => { document.body.append(d); d.showModal(); }}) {
  const enc = encodeURIComponent;
  let emailAvailable=true;
  let kinds = [], connections = [], runs = [], lastIndex = 0, indexPending = null, generation = 0;
  const label = (v) => typeof v === "object" && v ? v[lang] || v.en || v.zh || "" : v || "";
  const kindLabel = (kind) => label(kinds.find(k => k.name === kind)?.label) || tr(`kind.${kind}`, kind);
  function tr(key, fallback) { const result = t(`entry.${key}`); return result === `entry.${key}` ? fallback || key : result; }
  const fieldLabel = (f) => tr(`field.${f.name}`, label(f.label) || f.name);
  const when = value => value ? new Date(value).toLocaleString(lang === "zh" ? "zh-CN" : "en-US") : "";
  function safeLink(raw, text) {
    try { const u = new URL(raw, location.origin); if (!["https:", "http:"].includes(u.protocol)) return null;
      return h("a", {href:u.href, target:"_blank", rel:"noopener noreferrer"}, text); } catch { return null; }
  }
  const status = value => h("span", {class:`chip ${["dead","failed","refused"].includes(value)?"err":["unknown","awaiting_confirmation"].includes(value)?"warn":""}`}, tr(`state.${value}`, value));
  const errorBox = () => h("p", {class:"err-text", role:"alert"});
  function dialog(title, body, actions = []) {
    const d = h("dialog", {class:"entry-dialog", "aria-label":title},
      h("div", {class:"d-head"}, h("h2", {}, title)), body,
      h("div", {class:"d-foot"}, h("button", {type:"button", class:"btn ghost", onclick:()=>d.close()}, t("common.close")), ...actions));
    d.addEventListener("close", ()=>d.remove()); show(d); return d;
  }
  function reset() { generation++; document.querySelectorAll("dialog.entry-dialog").forEach(d=>{d.textContent="";d.close();d.remove();}); kinds=[]; connections=[]; runs=[]; lastIndex=0; indexPending=null; }
  async function refreshIndex(force = false) {
    if (!state.me || indexPending || (!force && Date.now()-lastIndex<15000)) return indexPending;
    const g=generation; lastIndex=Date.now();
    indexPending=api("GET", "/api/entry/runs").then(data=>{
      if (g!==generation) return;
      runs=data.triggers || []; changed();
    }).catch(()=>{}).finally(()=>{ if(g===generation) indexPending=null; });
    return indexPending;
  }
  function origin(sessionID) { return runs.find(r=>r.session_id===sessionID); }
  function sourceKind(sessionID) { return origin(sessionID)?.kind || "web"; }
  function sourceBadge(sessionID) {
    const run=origin(sessionID); if(!run) return null;
    return h("button", {type:"button",class:"btn ghost sm entry-source-badge", "data-entry-session":sessionID, onclick:()=>openRun(sessionID), title:tr("runDetails")},
      icon("link", "xs"), kindLabel(run.kind));
  }
  function filterOptions() { return ["web",...new Set(runs.map(r=>r.kind).filter(Boolean))]; }
  async function renderPage(host) {
    const g=generation;
    host.replaceChildren(h("p",{class:"muted",role:"status"},t("common.loading")));
    try {
      const [catalog, list]=await Promise.all([api("GET","/api/entry/kinds"),api("GET","/api/entry/connections")]);
      if (!host.isConnected || g!==generation) return;
      kinds=catalog.kinds || []; connections=list.connections || []; emailAvailable=catalog.email_delivery_available!==false;
      if(catalog.available===false){host.replaceChildren(h("p",{class:"muted"},tr("unavailable")));return;}
      drawPage(host);
    } catch(ex) { if(host.isConnected){const err=errorBox();err.textContent=ex.message;host.replaceChildren(err,h("button",{class:"btn sm",onclick:()=>renderPage(host)},tr("refresh")));} }
  }
  function drawPage(host) {
    const list=h("div",{class:"entry-connections"});
    list.replaceChildren(...connections.map(c=>{
      const err=errorBox();
      const toggle=h("button",{class:"btn sm",role:"switch","aria-checked":String(!!c.enabled),"aria-label":`${tr("enabled")} · ${c.name}`,onclick:async()=>{
        if(!c.enabled&&!emailAvailable&&(c.sinks||[]).some(x=>(x.kind||x.sink)==="email")){err.textContent=tr("emailUnavailable");return;}
        toggle.disabled=true;
        try{await api("PUT",`/api/entry/connections/${enc(c.id)}`,{...connectionBody(c),enabled:!c.enabled});await renderPage(host);}
        catch(ex){err.textContent=ex.message;toggle.disabled=false;}
      }},tr(c.enabled?"disable":"enable"));
      return h("article",{class:"entry-connection","data-entry-connection":c.id},
        h("div",{class:"entry-row"},h("div",{class:"grow"},h("h3",{},c.name),h("p",{class:"muted small"},`${kindLabel(c.kind)} · ${state.projects.find(p=>p.id===c.project_id)?.name || c.project_id}`)),
          h("span",{class:"chip"},tr(c.enabled?"enabled":"disabled")),toggle),
        h("p",{class:"entry-destinations muted small"},tr("destinations")+" · "+(c.sinks || []).map(sinkLabel).join(" · ")),
        c.webhook_url ? h("div",{class:"entry-url"},h("code",{},c.webhook_url),h("button",{class:"btn ghost xs",onclick:()=>copy(c.webhook_url)},t("common.copy"))) : null,
        h("div",{class:"entry-actions"},h("button",{class:"btn sm",onclick:()=>editConnection(c,host)},tr("edit")),
          h("button",{class:"btn ghost sm",onclick:()=>openHistory(c)},tr("history")),
          ["alert","email"].includes(c.kind)?h("button",{class:"btn ghost sm",onclick:async e=>{
            const button=e.currentTarget,epoch=generation; // read before the await: currentTarget is cleared afterwards
            if(!(await ask(tr("rotateConfirm"),false)))return;
            button.disabled=true;
            try{const result=await api("POST",`/api/entry/connections/${enc(c.id)}/rotate-secret`,{});if(result.secret&&epoch===generation)showSecret(result.secret,result.connection?.webhook_url||c.webhook_url);}
            catch(ex){err.textContent=ex.message;}finally{button.disabled=false;}
          }},tr("rotate")):null,
          h("button",{class:"btn ghost sm danger",onclick:async e=>{
            const button=e.currentTarget;
            if(!(await ask(tr("deleteConfirm", "Delete this connection?").replace("{name}",c.name))))return;
            button.disabled=true;
            try{await api("DELETE",`/api/entry/connections/${enc(c.id)}`);await renderPage(host);}catch(ex){err.textContent=ex.message;button.disabled=false;}
          }},tr("delete"))),err);
    }));
    host.replaceChildren(h("section",{class:"entry-page"},h("div",{class:"entry-row entry-heading"},
      h("div",{class:"grow"},h("h2",{},tr("title")),h("p",{class:"muted"},tr("lead"))),
      h("button",{class:"btn primary","data-entry-create":"",disabled:!kinds.some(k=>k.available!==false)||!state.projects.length,onclick:()=>editConnection(null,host)},icon("plus","sm"),tr("create"))),
      !state.projects.length?h("p",{class:"note"},tr("needWorkspace")):null,
      connections.length?list:h("div",{class:"entry-empty"},icon("link","lg"),h("h3",{},tr("empty")),h("p",{class:"muted"},tr("emptyHint")))));
  }
  function connectionBody(c) {
    return {name:c.name,kind:c.kind,project_id:c.project_id,enabled:!!c.enabled,config:c.config||{},sinks:c.sinks||[],language:c.language||lang,
      autonomy:c.autonomy||"analyze",runs_per_hour:c.runs_per_hour??c.limits?.runs_per_hour??3,budget_usd_per_hour:c.budget_usd_per_hour??c.limits?.budget_usd_per_hour??5};
  }
  function sinkLabel(s) {
    const kind=s.kind || s.sink, a=s.address||{};
    return tr(`sink.${kind}`,kind)+(kind==="github_comment"?(a.repo?` · ${a.repo} #${a.issue || ""}`:` · ${tr("replyOriginal")}`):kind==="email"?` · ${a.email || tr("replyOriginal")}`:"");
  }
  function editConnection(existing, host) {
    const c=existing?connectionBody(existing):{name:"",kind:kinds.find(k=>k.available!==false)?.name,project_id:state.projects[0]?.id,enabled:false,config:{},sinks:[{kind:"web",address:{}}],language:lang,autonomy:"analyze",runs_per_hour:3,budget_usd_per_hour:5};
    const text=(name,value,attrs={})=>h("input",{class:"field",name,value,...attrs});
    const select=(name,options,value)=>{const e=h("select",{class:"field",name},...options.map(o=>h("option",{value:o.value,disabled:o.disabled},o.label)));e.value=value;return e;};
    const fields={name:text("name",c.name,{required:true,maxlength:80}),kind:select("kind",kinds.map(k=>({value:k.name,label:kindLabel(k.name),disabled:k.available===false})),c.kind),
      project_id:select("project_id",state.projects.map(p=>({value:p.id,label:p.name})),c.project_id),
      language:select("language",[{value:"zh",label:"简体中文"},{value:"en",label:"English"}],c.language),
      autonomy:select("autonomy",["analyze","draft_pr","confirm"].map(value=>({value,label:tr(`autonomy.${value}`)})),c.autonomy),
      runs_per_hour:text("runs_per_hour",c.runs_per_hour,{type:"number",min:1,max:3,step:1,required:true}),
      budget_usd_per_hour:text("budget_usd_per_hour",c.budget_usd_per_hour,{type:"number",min:0.01,max:5,step:0.01,required:true})};
    fields.kind.disabled=!!existing;fields.project_id.disabled=!!existing;
    const enabled=h("input",{type:"checkbox",name:"enabled",checked:c.enabled,disabled:!existing});
    const dynamic=h("div",{class:"entry-fields"}), sinksHost=h("div",{class:"entry-sinks"}), err=errorBox();
    let configControls=[],sinkRows=[];
    const defaultSinks=kind=>[{kind:["github_comment","email"].includes(kind)?kind:"web",address:{}}];
    if(!existing)c.sinks=defaultSinks(c.kind);
    const dynamicConfig=()=>{
      const kind=kinds.find(k=>k.name===fields.kind.value), schemaFields=(kind?.fields || kind?.schema?.fields || []).filter(f=>!["language","autonomy"].includes(f.name));
      configControls=schemaFields.map(f=>{
        const current=fields.kind.value===c.kind ? c.config[f.name] : undefined;
        const value=current??f.default??"";
        const opts=f.options||f.enum||[];
        let input;
        if(opts.length) input=select(`config.${f.name}`,opts.map(o=>({value:typeof o==="object"?o.value:o,label:label(o.label)||String(o.value??o)})),value);
        else if(f.type==="boolean") input=h("input",{type:"checkbox",name:`config.${f.name}`,checked:!!value});
        else if(["textarea","multiline","array","string_array","string[]","object"].includes(f.type)||f.name==="instruction"||f.name==="instruction_template") input=h("textarea",{class:"field",name:`config.${f.name}`,rows:3,value:Array.isArray(value)?value.join("\n"):typeof value==="object"?JSON.stringify(value,null,2):value});
        else input=text(`config.${f.name}`,value,{type:["integer","number"].includes(f.type)?"number":f.type==="email"?"email":"text",step:f.type==="integer"?1:"any",min:f.min,max:f.max});
        input.required=!!f.required; if(f.max_length)input.maxLength=f.max_length;
        const row=h("label",{class:"lbl"},fieldLabel(f),input,label(f.description)?h("span",{class:"muted small"},label(f.description)):null);
        return {f,input,row};
      });
      dynamic.replaceChildren(...configControls.map(x=>x.row));
      const mention=configControls.find(x=>x.f.name==="mention");
      if(mention){const example=h("p",{class:"entry-mention-example muted small"});const update=()=>example.textContent=tr("mentionExample", "Mention: @{name} please do this · Confirm: @{name} confirm").replaceAll("{name}",mention.input.value.replace(/^@/,"")||"coding-agent");mention.input.addEventListener("input",update);update();dynamic.append(example);}
    };
    fields.kind.addEventListener("change",()=>{dynamicConfig();sinkRows=[];sinksHost.replaceChildren();defaultSinks(fields.kind.value).forEach(addSink);}); dynamicConfig();
    function addSink(value={kind:"web",address:{}}) {
      const kind=select("sink",["web","github_comment","email"].map(value=>({value,label:tr(`sink.${value}`)})),value.kind||value.sink||"web");
      const address=value.address || {};
      const repo=text("repo",address.repo||"",{placeholder:"owner/repository",pattern:"[^/\\s]+/[^/\\s]+"});
      const issue=text("issue",address.issue||"",{type:"number",min:1,step:1});
      const email=text("email",address.email||"",{type:"email"});
      const controls=h("div",{class:"entry-sink-address"});
      const original=h("input",{type:"checkbox",checked:kind.value===fields.kind.value&&![address.repo,address.issue,address.email].some(Boolean)});
      const originalRow=h("label",{class:"entry-check"},original,tr("replyOriginal"));
      const mailNote=h("p",{class:"note"},tr("emailUnavailable"));
      const draw=()=>{const canReply=["github_comment","email"].includes(kind.value)&&kind.value===fields.kind.value;originalRow.hidden=!canReply;
        const reply=canReply&&original.checked;repo.required=issue.required=kind.value==="github_comment"&&!reply;email.required=kind.value==="email"&&!reply;
        repo.disabled=issue.disabled=email.disabled=reply;mailNote.hidden=kind.value!=="email"||emailAvailable;
        controls.replaceChildren(...(kind.value==="github_comment"?[h("label",{class:"lbl"},tr("repository"),repo),h("label",{class:"lbl"},tr("issue"),issue)]:kind.value==="email"?[h("label",{class:"lbl"},tr("emailAddress"),email)]:[h("p",{class:"muted small"},tr("webOnly"))]));};
      kind.addEventListener("change",draw);original.addEventListener("change",draw);draw();
      const phaseControls=["accepted","question","result","failed"].map(phase=>({phase,input:h("input",{type:"checkbox",checked:(value.phases||["question","result","failed"]).includes(phase)})}));
      const phases=h("div",{class:"entry-phases"},...phaseControls.map(({phase,input})=>h("label",{class:"entry-check"},input,tr(`phase.${phase}`))));
      const item={kind,repo,issue,email,original,phaseControls};
      item.row=h("div",{class:"entry-sink"},h("div",{class:"entry-row"},h("label",{class:"lbl grow"},tr("destination"),kind),
        h("button",{type:"button",class:"btn ghost sm icon-only",title:tr("removeDestination"),onclick:()=>{sinkRows=sinkRows.filter(x=>x!==item);item.row.remove();}},icon("x","sm"))),originalRow,controls,mailNote,phases);
      sinkRows.push(item);sinksHost.append(item.row);
    }
    c.sinks.forEach(addSink);
    const form=h("form",{class:"entry-form"},h("div",{class:"d-body"},
      h("div",{class:"entry-fields"},h("label",{class:"lbl"},tr("name"),fields.name),h("label",{class:"lbl"},tr("kind"),fields.kind),h("label",{class:"lbl"},tr("workspace"),fields.project_id)),
      h("h3",{},tr("triggerConfiguration")),dynamic,
      h("h3",{},tr("destinations")),sinksHost,h("button",{type:"button",class:"btn ghost sm",onclick:()=>addSink()},icon("plus","xs"),tr("addDestination")),
      h("h3",{},tr("limits")),h("div",{class:"entry-fields entry-fields-two"},...[["autonomy","autonomy"],["language","language"],["runs_per_hour","runLimit"],["budget_usd_per_hour","budgetLimit"]].map(([key,title])=>h("label",{class:"lbl"},tr(title),fields[key]))),
      h("label",{class:"entry-check"},enabled,tr(existing?"enabled":"activateAfterSaving")),h("p",{class:"muted small"},tr("disabledHint")),err));
    const save=h("button",{type:"submit",class:"btn primary",form:"entry-connection-form"},tr("save"));form.id="entry-connection-form";
    const d=dialog(tr(existing?"edit":"create"),form,[save]);
    form.addEventListener("submit",async e=>{
      e.preventDefault();if(!form.reportValidity())return;err.textContent="";
      if(!sinkRows.length){err.textContent=tr("needDestination");return;}
      if(enabled.checked&&!emailAvailable&&sinkRows.some(x=>x.kind.value==="email")){err.textContent=tr("emailUnavailable");return;}
      if(sinkRows.some(x=>x.kind.value!=="web"&&!x.phaseControls.some(y=>y.input.checked))){err.textContent=tr("needPhase");return;}
      const config={};
      try{for(const {f,input} of configControls){const value=input.value;
        config[f.name]=f.type==="boolean"?input.checked:["number","integer"].includes(f.type)?Number(value):["array","string_array","string[]"].includes(f.type)?value.split("\n").map(x=>x.trim()).filter(Boolean):f.type==="object"?JSON.parse(value||"{}"):value.trim();}
      }catch{err.textContent=tr("invalidConfig");return;}
      const payload={...Object.fromEntries(Object.entries(fields).map(([key,input])=>[key,input.value])),enabled:enabled.checked,config,
        runs_per_hour:Number(fields.runs_per_hour.value),budget_usd_per_hour:Number(fields.budget_usd_per_hour.value),
        sinks:sinkRows.map(x=>({kind:x.kind.value,phases:x.phaseControls.filter(y=>y.input.checked).map(y=>y.phase),address:x.kind.value===fields.kind.value&&x.original.checked?{}:x.kind.value==="github_comment"?{repo:x.repo.value.trim(),issue:Number(x.issue.value)}:x.kind.value==="email"?{email:x.email.value.trim()}:{}}))};
      save.disabled=true;const epoch=generation;
      try {const result=await api(existing?"PUT":"POST",`/api/entry/connections${existing?"/"+enc(existing.id):""}`,payload);
        if(epoch!==generation)return;d.close(); await renderPage(host); if(result.secret&&epoch===generation)showSecret(result.secret,result.connection?.webhook_url||result.webhook_url);
      }catch(ex){err.textContent=ex.message;}finally{save.disabled=false;}
    });
  }
  function showSecret(secret,url) {
    const code=h("code",{class:"entry-secret"},secret);
    const d=dialog(tr("secretTitle"),h("div",{class:"d-body"},h("p",{},tr("secretHint")),code,
      h("button",{class:"btn sm",onclick:()=>copy(code.textContent)},t("common.copy")),
      url?h("div",{class:"entry-url"},h("span",{class:"muted"},tr("webhookURL")),h("code",{},url)):null));
    d.addEventListener("close",()=>{code.textContent="";secret="";});
  }
  function triggerRow(run) {
    const source=safeLink(run.source_url,tr("original"));
    return h("article",{class:"entry-history-row"},h("div",{class:"entry-row"},h("strong",{class:"grow"},kindLabel(run.kind)),status(run.state)),
      h("p",{class:"muted small"},when(run.created_at)),run.reason?h("p",{},run.reason):null,
      h("div",{class:"entry-actions"},source,run.session_id?h("button",{class:"btn ghost sm",onclick:()=>openRun(run.session_id)},tr("runDetails")):null,
        run.project_id&&run.session_id?h("button",{class:"btn sm",onclick:()=>{document.querySelectorAll("dialog.entry-dialog").forEach(d=>d.close());openSession(run.project_id,run.session_id);}},tr("openSession")):null));
  }
  async function openHistory(c) {
    const body=h("div",{class:"d-body entry-history"},h("p",{class:"muted"},t("common.loading")));
    const refresh=h("button",{class:"btn sm",onclick:()=>load()},tr("refresh"));const d=dialog(`${c.name} · ${tr("history")}`,body,[refresh]);
    async function load(){refresh.disabled=true;try{const data=await api("GET",`/api/entry/connections/${enc(c.id)}/triggers`);if(d.isConnected)body.replaceChildren(...((data.triggers||[]).length?data.triggers.map(triggerRow):[h("p",{class:"muted"},tr("noTriggers"))]));}catch(ex){if(d.isConnected){const err=errorBox();err.textContent=ex.message;body.replaceChildren(err);}}finally{refresh.disabled=false;}}
    await load();
  }
  async function openRun(sessionID) {
    const body=h("div",{class:"d-body entry-history"},h("p",{class:"muted"},t("common.loading"))),err=errorBox();
    const refresh=h("button",{class:"btn sm",onclick:()=>load()},tr("refresh"));
    const d=dialog(tr("runDetails"),body,[refresh]);
    async function load(){refresh.disabled=true;try{const data=await api("GET",`/api/entry/runs?session=${enc(sessionID)}`);if(!d.isConnected)return;
      const deliveries=data.deliveries||[];
      body.replaceChildren(...(data.triggers||[]).map(run=>h("div",{class:"entry-history-row"},h("div",{class:"entry-row"},h("strong",{class:"grow"},kindLabel(run.kind)),status(run.state)),
        run.source_url?safeLink(run.source_url,tr("original")):null,run.reason?h("p",{},run.reason):null,
        run.state==="awaiting_confirmation"?h("button",{class:"btn primary sm",onclick:async e=>{
          const button=e.currentTarget;
          if(!(await ask(tr("confirmRunPrompt"),false)))return;
          button.disabled=true;err.textContent="";
          try{await api("POST",`/api/entry/triggers/${enc(run.id)}/confirm`,{});await load();}catch(ex){err.textContent=ex.message;button.disabled=false;}
        }},tr("confirmRun")):null)),h("h3",{},tr("deliveries")),
        ...deliveries.map(item=>h("article",{class:"entry-history-row"},h("div",{class:"entry-row"},h("strong",{class:"grow"},tr(`phase.${item.phase}`,item.phase)),status(item.state)),
          h("p",{class:"muted small"},`${sinkLabel({kind:item.kind||item.sink,address:item.address})} · ${tr("attempts").replace("{n}",item.attempts||0)}`),
          (item.last_error||item.reason)?h("p",{},item.last_error||item.reason):null,
          item.state==="unknown"?h("p",{class:"note"},tr("unknownHint")):null,
          item.state==="dead"?h("button",{class:"btn sm",onclick:async e=>{
            e.currentTarget.disabled=true;err.textContent="";
            try{await api("POST",`/api/entry/deliveries/${enc(item.id||item.key)}/retry`,{});await load();}catch(ex){err.textContent=ex.message;e.target.disabled=false;}
          }},tr("retry")):null)),!deliveries.length?h("p",{class:"muted"},tr("noDeliveries")):null,err);
    }catch(ex){if(d.isConnected){err.textContent=ex.message;if(!err.isConnected)body.append(err);}}finally{refresh.disabled=false;}}
    await load();const timer=setInterval(()=>{if(d.isConnected&&!document.hidden)load();},10000);d.addEventListener("close",()=>clearInterval(timer));
  }
  return {renderPage, refreshIndex, origin, sourceKind, sourceBadge, filterOptions, kindLabel, reset};
}

import React,{useCallback,useEffect,useRef,useState} from 'react';
import {Button,Input,Tabs,TextArea} from '@heroui/react';
import {CodeBlock} from '@heroui-pro/react/code-block';
import type {ContextSource} from './context-panel';
import './file-workbench.css';

type Source=ContextSource&{file_id?:string;version_id?:string};
type Version={version_id:string;id?:string;mime_type:string;size_bytes:number;sha256:string;created_at:string;parent_version_id?:string};
type Artifact={id?:string;artifact_id?:string;name?:string;path?:string;mime_type?:string};
type Block={id:string;kind?:string;text?:string;anchor?:Record<string,unknown>};
type Job={id:string;operation?:string;status:string;result?:unknown;consumer_id?:string;error?:unknown;created_at?:string};
type Change={changeset_id:string;expected_head:string;verification_digest?:string;status:string;candidate_sha256?:string;required_fidelity?:string;published_version?:string};
type Details={file:{file_id?:string;id?:string;name:string;head_version_id:string;created_by:string;lifecycle:string;acl_epoch?:number};versions:Version[];grants:Array<{subject_id:string;permission:string}>;jobs:Job[];changesets:Change[];permissions?:{read:boolean;write:boolean;manage:boolean}};
type Result={status?:string;blocks?:Block[];outline?:unknown;coverage?:unknown;warnings?:unknown[];artifacts?:Artifact[];rows?:unknown[];columns?:unknown[];capabilities?:unknown;verification?:unknown;[key:string]:unknown};
export interface FileWorkbenchProps {base:string;sources:Source[];initialSourceID?:string;onClose:()=>void;onUse:(source:Source)=>void;onChanged:()=>void;onUseCollection?:(collection:{id:string;roots?:unknown[]})=>void;language?:string}
const done=new Set(['succeeded','completed','done','failed','cancelled','canceled','error','partial','unsupported']);
function unpack(value:unknown):Result {if(typeof value==='string'){try{return JSON.parse(value);}catch{return {message:value};}}return value&&typeof value==='object'?value as Result:{};}
function printable(value:unknown){return typeof value==='string'?value:JSON.stringify(value,null,2)??'';}
function fid(source:Source){return source.file_id||source.id;}
function size(bytes?:number){if(bytes==null)return '';return bytes<1024?bytes+' B':bytes<1048576?(bytes/1024).toFixed(0)+' KB':(bytes/1048576).toFixed(1)+' MB';}

function dateText(value:unknown,zh:boolean){if(value==null)return '';const date=new Date(value as any);return Number.isNaN(date.getTime())?String(value):date.toLocaleString(zh?'zh-CN':'en',{dateStyle:'medium',timeStyle:'short'});}
function coverageText(value:unknown,zh:boolean):string {
 if(value==null)return '';if(typeof value!=='object')return String(value);
 const c=value as Record<string,any>,parts:string[]=[];
 const pages=c.pages_read||c.pages||c.processed_pages;
 if(Array.isArray(pages))parts.push((zh?'已处理 ':'Processed ')+pages.length+(zh?' 页':' pages')+(c.total_pages?(zh?' / 共 ':' / ')+c.total_pages+(zh?' 页':' total'):''));
 for(const [key,cn,en] of [['rows_scanned','扫描行数','Rows scanned'],['rows_read','读取行数','Rows read'],['blocks','内容块','Blocks'],['characters','字符','Characters']] as const)if(typeof c[key]==='number')parts.push((zh?cn:en)+' '+c[key]);
 if(c.inspected===true)parts.push(zh?'文件检查完成':'File inspected');
 if(c.partial===true||c.status==='partial')parts.push(zh?'部分覆盖':'Partial coverage');
 return parts.join(' · ')||(zh?'处理范围已记录，完整信息见下方处理结果。':'Coverage is recorded in the full processing result below.');
}

function icon(kind:'file'|'close'|'back'|'refresh'|'trash'|'arrow') {const paths={file:'M6 2h7l5 5v15H6z M13 2v6h5 M9 12h6 M9 16h6',close:'M6 6l12 12 M18 6L6 18',back:'M15 5l-7 7 7 7',refresh:'M19 8a8 8 0 1 0 1 7 M19 3v5h-5',trash:'M4 7h16 M9 7V4h6v3 M7 7l1 14h8l1-14',arrow:'M5 12h14 M13 6l6 6-6 6'};return <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[kind]}/></svg>;}
function JsonView({value,label}:{value:unknown;label:string}){return <CodeBlock className="fw-json"><CodeBlock.Header><span>{label}</span></CodeBlock.Header><pre tabIndex={0}>{printable(value)}</pre></CodeBlock>;}
function DataTable({rows,columns}:{rows:unknown[];columns:unknown[]}){
 const keys=columns.map(column=>typeof column==='string'?column:String((column as any)?.name??column));
 const cols=keys.length?keys:rows[0]&&typeof rows[0]==='object'&&!Array.isArray(rows[0])?Object.keys(rows[0] as object):Array.isArray(rows[0])?rows[0].map((_,i)=>String(i+1)):['value'];
 return <div className="fw-table-scroll"><table><thead><tr>{cols.map(col=><th key={col}>{col}</th>)}</tr></thead><tbody>{rows.map((row,i)=><tr key={i}>{cols.map((col,j)=><td key={col}>{printable(Array.isArray(row)?row[j]:row&&typeof row==='object'?(row as any)[col]:row)}</td>)}</tr>)}</tbody></table></div>;
}

export function FileWorkbench(props:FileWorkbenchProps){
 const zh=(props.language||document.documentElement.lang||navigator.language).startsWith('zh');
 const t=(cn:string,en:string)=>zh?cn:en;
 const [localSources,setLocalSources]=useState(props.sources);
 useEffect(()=>setLocalSources(props.sources),[props.sources]);
 const [selected,setSelected]=useState(props.initialSourceID||props.sources[0]?.id||'');
 const [search,setSearch]=useState(''),[tab,setTab]=useState('preview'),[trash,setTrash]=useState(false),[trashFiles,setTrashFiles]=useState<Source[]>([]);
 const [details,setDetails]=useState<Details|null>(null),[loading,setLoading]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('');
 const [versionID,setVersionID]=useState(''),[job,setJob]=useState<Job|null>(null),[busy,setBusy]=useState(false);
 const [pages,setPages]=useState('1-3'),[sheet,setSheet]=useState(''),[range,setRange]=useState(''),[sql,setSQL]=useState('SELECT * FROM data LIMIT 100'),[editJSON,setEditJSON]=useState('[]');
 const [member,setMember]=useState(''),[rename,setRename]=useState(''),[subject,setSubject]=useState(''),[permission,setPermission]=useState('read');
 const [lineStart,setLineStart]=useState('1'),[lineCount,setLineCount]=useState('200');
 const [indexSources,setIndexSources]=useState<string[]>([]);
 const [editMode,setEditMode]=useState('value'),[cell,setCell]=useState('B2'),[cellValue,setCellValue]=useState(''),[paragraph,setParagraph]=useState('0'),[slide,setSlide]=useState('0'),[shape,setShape]=useState(''),[oldText,setOldText]=useState(''),[newText,setNewText]=useState(''),[advancedEdit,setAdvancedEdit]=useState(false);
 const [knowledgeQuery,setKnowledgeQuery]=useState(''),[knowledgeHits,setKnowledgeHits]=useState<any[]>([]),[collection,setCollection]=useState<any>(null);
 const alive=useRef(true),epoch=useRef(0),controllers=useRef(new Set<AbortController>());
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;for(const controller of controllers.current)controller.abort();};},[]);
 const api=useCallback(async(path:string,method='GET',body?:unknown,signal?:AbortSignal)=>{
  const url=new URL(props.base.replace(/\/$/,'')+path,location.href);if(url.origin!==location.origin)throw Error('File service must use the application origin');
  const controller=new AbortController();controllers.current.add(controller);const abort=()=>controller.abort();signal?.addEventListener('abort',abort,{once:true});
  try{const response=await fetch(url,{method,credentials:'same-origin',headers:{'X-Coding-Web':'1',...(body===undefined?{}:{'Content-Type':'application/json'})},body:body===undefined?undefined:JSON.stringify(body),signal:controller.signal});
   const text=await response.text();let data:any;try{data=text?JSON.parse(text):{};}catch{data={error:text};}
   if(!response.ok){const error=Object.assign(Error(typeof data.error==='string'?data.error:typeof data.message==='string'?data.message:`HTTP ${response.status}`),{status:response.status});throw error;}return data;
  }finally{controllers.current.delete(controller);signal?.removeEventListener('abort',abort);}
 },[props.base]);
 useEffect(()=>{let active=true;void api('/knowledge/collection').then(value=>{if(active)setCollection(value);}).catch(()=>{});return()=>{active=false;};},[api]);
 const current=localSources.find(source=>source.id===selected||fid(source)===selected);
 const selectedFile=current?fid(current):selected;
 const chosen=details?.versions.find(version=>(version.version_id||version.id)===versionID);
 const mime=chosen?.mime_type||current?.mime_type||'';
 const result=unpack(job?.result);
 const working=!!job&&!done.has(job.status)&&job.status!=='poll_error';
 const protectedAction=busy||working||!details||details.file.lifecycle!=='active';
 const writeBlocked=protectedAction||details?.permissions?.write!==true;
 const manageBlocked=protectedAction||details?.permissions?.manage!==true;
 const changed=()=>{props.onChanged();};
 const loadDetails=useCallback(async(id:string,signal?:AbortSignal)=>{
  const data=await api('/files/'+encodeURIComponent(id)+'/details','GET',undefined,signal);
  return {file:data.file,versions:data.versions||[],grants:data.grants||[],jobs:(data.jobs||[]).map((entry:any)=>({...entry,id:entry.id||entry.job_id})),changesets:data.changesets||[],permissions:data.permissions} as Details;
 },[api]);
 useEffect(()=>{
  const controller=new AbortController(),serial=++epoch.current;
  setDetails(null);setJob(null);setVersionID('');setKnowledgeHits([]);setError('');setNotice('');setLoading(!!selectedFile);
  if(selectedFile)loadDetails(selectedFile,controller.signal).then(data=>{if(serial===epoch.current&&!controller.signal.aborted){setDetails(data);setVersionID(data.file.head_version_id);setRename(data.file.name);setLoading(false);}}).catch(ex=>{if(!controller.signal.aborted&&serial===epoch.current){setError(ex.message);setLoading(false);}});
  return()=>controller.abort();
 },[selectedFile,loadDetails]);
 const refresh=async()=>{if(!selectedFile)return;const serial=epoch.current;const data=await loadDetails(selectedFile);if(alive.current&&epoch.current===serial){setDetails(data);setVersionID(previous=>data.versions.some(version=>(version.version_id||version.id)===previous)?previous:data.file.head_version_id);}};
 useEffect(()=>{
  if(!working||!job)return;const controller=new AbortController(),serial=epoch.current,id=job.id;let timer:ReturnType<typeof setTimeout>;
  const poll=async()=>{try{
   const data=await api('/file-jobs/'+encodeURIComponent(id),'GET',undefined,controller.signal);
   if(controller.signal.aborted||serial!==epoch.current)return;
   const updated={...job,...data,id};
   if(done.has(updated.status)){
    const fresh=await loadDetails(selectedFile,controller.signal);
    if(!controller.signal.aborted&&serial===epoch.current){setDetails(fresh);setJob(updated);props.onChanged();}
   }else {setJob(updated);timer=setTimeout(poll,2000);}
  }catch(ex){if(!controller.signal.aborted&&serial===epoch.current){setError((ex as Error).message);setJob(previous=>previous?.id===id?{...previous,status:'poll_error'}:previous);}}};
  timer=setTimeout(poll,2000);return()=>{controller.abort();clearTimeout(timer);};
 },[job?.id,working,api,selectedFile,loadDetails]);
 const guard=async(action:()=>Promise<void>)=>{if(busy)return;setBusy(true);setError('');setNotice('');const serial=epoch.current;try{await action();}catch(ex){if(alive.current&&serial===epoch.current)setError((ex as Error).message);}finally{if(alive.current)setBusy(false);}};
 const selector=()=>{
  if(/pdf/.test(mime)){const found=new Set<number>();for(const part of pages.split(',')){const match=/^\s*(\d+)(?:\s*-\s*(\d+))?\s*$/.exec(part);if(!match)throw Error(t('页码格式应为 1-3,5','Use page ranges such as 1-3,5'));const a=Number(match[1]),b=Number(match[2]||match[1]);if(a<1||b<a||b-a>=50)throw Error(t('单次最多选择 50 页','Select up to 50 pages per operation'));for(let n=a;n<=b;n++)found.add(n-1);}if(found.size>50)throw Error(t('单次最多选择 50 页','Select up to 50 pages per operation'));return {pages:[...found]};}
  if(/spreadsheet|excel/.test(mime))return {...(sheet?{sheet}:{}),...(range?{range}:{})};
  if(/^text\//.test(mime)||/json|javascript|xml/.test(mime)){const start=Number(lineStart),count=Number(lineCount);if(!Number.isInteger(start)||!Number.isInteger(count)||start<1||count<1||count>500)throw Error(t('请输入有效行号和 1–500 行范围','Enter a start line and a range of 1–500 lines'));return {start_line:start,max_lines:count};}
  return {};
 };
 const editOperations=()=>{
  if(advancedEdit){let operations:unknown;try{operations=JSON.parse(editJSON);}catch{throw Error(t('编辑操作不是有效 JSON','Edit operations must be valid JSON'));}if(!Array.isArray(operations)||!operations.length)throw Error(t('至少填写一项编辑操作','Add at least one edit operation'));return operations;}
  if(/spreadsheet|excel/.test(mime)){
   if(!sheet.trim()||!/^\$?[A-Z]+\$?[1-9][0-9]*$/i.test(cell))throw Error(t('请输入工作表名和有效单元格地址','Enter a sheet name and a valid cell address'));
   if(editMode==='formula'){if(!cellValue.startsWith('='))throw Error(t('公式必须以 = 开头','Formula must start with ='));return [{type:'set_formula',sheet:sheet.trim(),cell,formula:cellValue}];}
   if(editMode==='number'&&(!cellValue.trim()||!Number.isFinite(Number(cellValue))))throw Error(t('请输入有效数值','Enter a finite number'));return [{type:'set_cell',sheet:sheet.trim(),cell,value:editMode==='number'?Number(cellValue):cellValue}];
  }
  if((/wordprocessingml/.test(mime)&&(!Number.isInteger(Number(paragraph))||Number(paragraph)<0))||(/presentationml/.test(mime)&&(!Number.isInteger(Number(slide))||Number(slide)<0)))throw Error(t('序号必须为非负整数','Index must be a nonnegative integer'));
  if(!oldText)throw Error(t('填写需替换的原文，以验证修改位置','Enter the expected text to verify the edit location'));
  if(/wordprocessingml/.test(mime))return [{type:'replace_text',paragraph_index:Number(paragraph),expected_text:oldText,text:newText,occurrences:1}];
  if(/presentationml/.test(mime)){if(!shape.trim())throw Error(t('先从结构读取结果中选择形状 ID','Enter a shape ID from the structure result'));return [{type:'replace_text',slide_index:Number(slide),shape_id:shape.trim(),expected_text:oldText,text:newText,occurrences:1}];}
  throw Error(t('此类型请使用已支持的高级编辑操作','Use a supported advanced edit operation for this file type'));
 };
 const start=async(operation:string,options:Record<string,unknown>={})=>guard(async()=>{
  if(['edit','edit_prepare','recalculate','notebook_execute'].includes(operation)&&details?.permissions?.write!==true)throw Error(t('此文件没有编辑权限','You do not have write access to this file'));
  if(!selectedFile||!versionID)throw Error(t('请选择文件版本','Select a file version'));
  const serial=epoch.current;let selected:Record<string,unknown>=selector();
  if(operation==='extract'){const members=member.split('\n').map(item=>item.trim()).filter(Boolean);if(!members.length)throw Error(t('先填写需要提取的成员路径','Enter member paths to extract'));selected={members};}
  if(operation==='edit'||operation==='edit_prepare')options={base_sha256:chosen?.sha256,fidelity:'L1',operations:editOperations()};
  const requestID=crypto.randomUUID();const data=await api('/file-jobs','POST',{operation,request_id:requestID,inputs:[{file_id:selectedFile,version_id:versionID}],selector:selected,options});
  if(serial!==epoch.current||!alive.current)return;setJob({...data,id:data.id||data.job_id,operation,consumer_id:data.consumer_id||data.request_id||requestID});if(done.has(data.status)){await refresh();changed();}
 });
 const indexCurrent=()=>guard(async()=>{
  const serial=epoch.current,selection=indexSources.length?indexSources:[selectedFile];
  if(selection.length>500)throw Error(t('一个集合最多选择 500 份资料','Select up to 500 sources'));
  const jobs=await Promise.all(selection.map(async id=>{
   const detail=await loadDetails(id);
   const latest=detail.jobs.find(entry=>entry.operation==='read'&&['succeeded','completed','done','partial'].includes(entry.status));
   const active=id===selectedFile&&job?.operation==='read'&&['succeeded','completed','done','partial'].includes(job.status)?job:null;
   const selectedJob=active||latest;
   if(!selectedJob)throw Error(detail.file.name+' — '+t('请先读取文件，不能把未解析资料加入集合。','Read this file before adding it to the collection.'));
   return selectedJob.id;
  }));
  let previous:any=null;try{previous=await api('/knowledge/collection');}catch(ex){if((ex as any).status!==404)throw ex;}
  if(serial!==epoch.current||!alive.current)return;
  const data=await api('/knowledge/index','POST',{job_ids:[...new Set(jobs)],expected_revision:previous?.revision||0});
  if(serial!==epoch.current||!alive.current)return;setCollection(data.collection);setKnowledgeHits([]);setNotice(t('已用所选资料构建检索集合','Search collection built from selected sources'));changed();
 });
 const searchKnowledge=()=>guard(async()=>{const serial=epoch.current;const data=await api('/knowledge/search','POST',{query:knowledgeQuery,limit:8});if(serial!==epoch.current||!alive.current)return;setKnowledgeHits(data.hits||data.evidence||[]);if(!(data.hits||data.evidence||[]).length)setNotice(t('没有找到当前有权读取的匹配证据。','No matching evidence is currently available to you.'));});
 const action=(kind:string,extra:Record<string,unknown>={})=>guard(async()=>{if(details?.permissions?.manage!==true)throw Error(t('此操作需要文件管理权限','Managing this file requires owner access'));const target=selectedFile,serial=epoch.current;await api('/files/'+encodeURIComponent(target)+'/actions','POST',{action:kind,...extra});if(serial!==epoch.current||!alive.current)return;if(kind==='rename')setLocalSources(items=>items.map(source=>fid(source)===target?{...source,name:String(extra.name)}:source));if(kind==='trash'){setLocalSources(items=>items.filter(source=>fid(source)!==target));setNotice(t('文件已移入回收站','File moved to trash'));setDetails(null);}else await refresh();changed();});
 const showTrash=async(value:boolean)=>guard(async()=>{setTrash(value);if(value){const data=await api('/files/trash');setTrashFiles((data.files||[]).map((item:any)=>{const file=item.file||item;return {id:file.file_id||file.id,file_id:file.file_id||file.id,name:file.name,mime_type:item.version?.mime_type,version_id:file.head_version_id};}));}});
 const restore=(source:Source)=>guard(async()=>{await api('/files/'+encodeURIComponent(fid(source))+'/actions','POST',{action:'restore'});setTrashFiles(items=>items.filter(item=>item.id!==source.id));setLocalSources(items=>items.some(item=>fid(item)===fid(source))?items:[...items,source]);changed();});
 const download=(version=versionID)=>props.base+'/files/'+encodeURIComponent(selectedFile)+'/versions/'+encodeURIComponent(version)+'/content';
 const artifactURL=(artifact:Artifact)=>job&&(artifact.id||artifact.artifact_id)?props.base+'/file-jobs/'+encodeURIComponent(job.id)+'/artifacts/'+encodeURIComponent(artifact.id||artifact.artifact_id!):undefined;
 const files=(trash?trashFiles:localSources).filter(source=>source.name.toLocaleLowerCase().includes(search.toLocaleLowerCase()));
 const jobLabel=(status:string)=>({queued:t('排队中','Queued'),running:t('处理中','Processing'),succeeded:t('已完成','Completed'),completed:t('已完成','Completed'),failed:t('处理失败','Failed'),cancelled:t('已取消','Cancelled'),partial:t('部分完成','Partial'),unsupported:t('此操作不受支持','Unsupported')}[status]||status);
 const resultArea=<>
  {result.coverage!=null&&<div className="fw-coverage"><span>{t('覆盖范围','Coverage')}</span><span>{coverageText(result.coverage,zh)}</span></div>}
  {!!result.warnings?.length&&<ul className="fw-warnings">{result.warnings.map((warning,i)=><li key={i}>{printable(warning)}</li>)}</ul>}
  {(job?.status==='failed'||job?.status==='error')&&<div className="fw-error" role="alert">{printable(job.error||result.error||result.message||t('处理失败，请查看执行记录','Processing failed. Review the execution record.'))}</div>}
 </>;
 return <div className="file-workbench" data-pro-component="FileWorkbench">
  <header className="fw-header"><div><span className="fw-eyebrow">{t('文件工作台','File workspace')}</span><h2>{t('从资料到结果','Work with your sources')}</h2></div><Button variant="ghost" isIconOnly aria-label={t('关闭工作台','Close file workspace')} onPress={props.onClose}>{icon('close')}</Button></header>
  <div className="fw-layout"><aside className="fw-sidebar">
   <div className="fw-library-switch"><Button size="sm" variant={trash?'ghost':'secondary'} onPress={()=>void showTrash(false)}>{t('来源','Sources')} <span>{localSources.length}</span></Button><Button size="sm" variant={trash?'secondary':'ghost'} onPress={()=>void showTrash(true)}>{icon('trash')}{t('回收站','Trash')}</Button></div>
   <Input value={search} onChange={event=>setSearch(event.target.value)} aria-label={t('搜索文件','Search files')} placeholder={t('查找文件…','Find a file…')}/>
   <div className="fw-file-list">{files.map(source=><div className="fw-file-item" key={source.id} data-selected={source.id===selected}><Button variant="ghost" className="fw-file-select" onPress={()=>{setSelected(source.id);setTrash(false);}} isDisabled={trash}><span className="fw-file-icon">{icon('file')}</span><span><strong title={source.name}>{source.name}</strong><small>{size(source.size_bytes)||source.mime_type?.split('/').pop()||t('文件','File')}</small></span></Button>{trash&&<Button size="sm" variant="ghost" onPress={()=>void restore(source)}>{t('恢复','Restore')}</Button>}</div>)}{!files.length&&<p className="fw-empty small">{trash?t('回收站为空','Trash is empty'):t('没有匹配的文件','No matching files')}</p>}</div>
  </aside><main className="fw-main">
   {error&&<div className="fw-error" role="alert">{error}<Button size="sm" variant="ghost" onPress={()=>setError('')}>{t('收起','Dismiss')}</Button></div>}{notice&&<p className="fw-notice" role="status">{notice}</p>}
   {!selectedFile?<div className="fw-empty">{icon('file')}<h3>{t('选择一份资料开始','Select a source to begin')}</h3><p>{t('读取、查看页面、分析数据或准备一个新版本。','Read, preview pages, analyze data or prepare a new version.')}</p></div>:loading?<div className="fw-empty" role="status">{t('正在读取文件信息…','Loading file details…')}</div>:!details?<div className="fw-empty"><p>{t('文件信息暂不可用','File details unavailable')}</p><Button variant="secondary" onPress={()=>void guard(refresh)}>{t('重新读取','Reload')}</Button></div>:<>
    <div className="fw-document-head"><div><h3>{details.file.name}</h3><p>{size(chosen?.size_bytes)} <span>·</span> {details.file.name.split('.').pop()?.toUpperCase()||t('文件','File')}</p></div><div className="fw-head-actions"><Button size="sm" variant="ghost" isIconOnly aria-label={t('刷新文件信息','Refresh file details')} onPress={()=>void guard(refresh)}>{icon('refresh')}</Button>{current&&<Button size="sm" variant="secondary" onPress={()=>props.onUse(current)} isDisabled={details.file.lifecycle!=='active'}>{t('用于下一条任务','Use in next task')}</Button>}<a className="fw-download" href={download()} download>{t('下载','Download')}</a></div></div>
    <Tabs variant="secondary" selectedKey={tab} onSelectionChange={key=>setTab(String(key))} className="fw-tabs"><Tabs.ListContainer><Tabs.List aria-label={t('文件视图','File views')}>{[['preview',t('预览','Preview')],['structure',t('结构','Structure')],['data',t('数据与编辑','Data & edits')],['versions',t('版本与权限','Versions & access')]].map(([id,label])=><Tabs.Tab key={id} id={id}>{label}<Tabs.Indicator/></Tabs.Tab>)}</Tabs.List></Tabs.ListContainer>
     <div className="fw-selection"><label>{t('文件版本','Version')}<select value={versionID} onChange={event=>{epoch.current++;setVersionID(event.target.value);setJob(null);setKnowledgeHits([]);}}>{details.versions.map(version=><option key={version.version_id||version.id} value={version.version_id||version.id}>{(version.version_id||version.id)?.slice(0,15)}{(version.version_id||version.id)===details.file.head_version_id?' · '+t('当前','Current'):''}</option>)}</select></label>
      {/pdf/.test(mime)?<label>{t('页码','Pages')}<Input value={pages} onChange={event=>setPages(event.target.value)} placeholder="1-3,5"/></label>:/spreadsheet|excel/.test(mime)?<><label>{t('工作表','Sheet')}<Input value={sheet} onChange={event=>setSheet(event.target.value)} placeholder="Sheet1"/></label><label>{t('范围','Range')}<Input value={range} onChange={event=>setRange(event.target.value)} placeholder="A1:F20"/></label></>:<><label>{t('起始行','Start line')}<Input type="number" min="1" value={lineStart} onChange={event=>setLineStart(event.target.value)}/></label><label>{t('行数','Lines')}<Input type="number" min="1" max="500" value={lineCount} onChange={event=>setLineCount(event.target.value)}/></label></>}
     </div>
     {job&&<div className="fw-job" data-job-status={job.status} role="status"><span className="fw-status-dot" data-active={working}/><strong>{jobLabel(job.status)}</strong><span>{({read:t('读取选区','Read selection'),render:t('页面预览','Page preview'),inspect:t('文件检查','Inspect'),query:t('数据查询','Query'),edit:t('准备修改','Edit'),recalculate:t('公式重算','Recalculate')} as Record<string,string>)[job.operation||'']||job.operation||''}</span>{working&&<Button size="sm" variant="ghost" isDisabled={busy} onPress={()=>void guard(async()=>{await api('/file-jobs/'+encodeURIComponent(job.id)+'/cancel','POST',{consumer_id:job.consumer_id});const updated=await api('/file-jobs/'+encodeURIComponent(job.id));setJob({...job,...updated});})}>{t('取消','Cancel')}</Button>}{job.status==='poll_error'&&<Button size="sm" variant="ghost" onPress={()=>setJob({...job,status:'running'})}>{t('重新连接','Reconnect')}</Button>}</div>}
     <Tabs.Panel id="preview"><div className="fw-action-row"><Button size="sm" variant="secondary" isDisabled={protectedAction} onPress={()=>void start('inspect')}>{t('检查文件','Inspect file')}</Button><Button size="sm" variant="primary" isDisabled={protectedAction} onPress={()=>void start('render')}>{t('生成页面预览','Render preview')}</Button></div>{resultArea}
      {result.artifacts?.length?<div className="fw-artifacts">{result.artifacts.map((artifact,i)=>{const url=artifactURL(artifact);return <figure key={artifact.id||artifact.path||i}>{url&&/^image\/(png|jpeg|webp)$/.test(artifact.mime_type||'')?<img src={url} alt={artifact.name||t('页面预览','Page preview')} loading="lazy"/>:icon('file')}<figcaption>{artifact.name||artifact.path||t('输出文件','Output')}{url&&<a href={url} target="_blank" rel="noopener noreferrer" download>{t('打开','Open')}</a>}</figcaption></figure>;})}</div>:<div className="fw-empty"><p>{t('原件已保存，尚未生成页面预览。','The original is stored. No page preview has been generated.')}</p><small>{t('点击“检查文件”了解能力，或生成所选页面的预览。','Inspect the file for available operations or render the selected pages.')}</small></div>}
      {result.capabilities!=null&&<JsonView value={result.capabilities} label={t('实际可用能力','Available capabilities')}/>}</Tabs.Panel>
     <Tabs.Panel id="structure"><div className="fw-action-row"><Button size="sm" variant="primary" isDisabled={protectedAction} onPress={()=>void start('read')}>{t('读取选区','Read selection')}</Button><Button size="sm" variant="ghost" isDisabled={protectedAction} onPress={()=>void start('archive_list')}>{t('列出归档成员','List archive')}</Button></div>{resultArea}
      {result.outline!=null&&<details className="fw-disclosure"><summary>{t('目录','Outline')}</summary><JsonView value={result.outline} label={t('结构目录','Document outline')}/></details>}
      {result.blocks?.length?<div className="fw-blocks">{result.blocks.map(block=><article key={block.id}><header><span>{({heading:t('标题','Heading'),paragraph:t('段落','Paragraph'),table:t('表格','Table'),code:t('代码','Code')} as Record<string,string>)[block.kind||'']||t('内容块','Block')}</span><code>{block.id}</code></header><p>{block.text}</p>{block.anchor&&<details><summary>{t('原文位置','Source location')}</summary><pre>{printable(block.anchor)}</pre></details>}</article>)}</div>:<p className="fw-empty">{t('尚未读取结构内容。','No structured content has been read.')}</p>}
      <section className="fw-knowledge fw-tool-section"><h4>{t('检索资料','Search your sources')}</h4><p className="fw-index-coverage">{t('索引覆盖已读取选区，未读取的页或范围不会参与检索。','The index covers only the selections already read. Unread pages and ranges are excluded from search.')}</p><p className="fw-muted">{t('选择已读取的资料构建集合，检索带原文位置的证据。重新构建会替换当前集合。','Build a collection from selected read results and retrieve evidence with source locations. Rebuilding replaces the current collection.')}</p><details className="fw-disclosure fw-index-selection"><summary>{t('选择集合资料（最多 500 份）','Choose collection sources (up to 500)')}</summary><p className="fw-muted">{t('未勾选时使用当前文件；每份资料必须已有完成的读取记录。','With no selection, use the current file. Each source needs a completed read.')}</p>{localSources.map(source=><label key={fid(source)}><input type="checkbox" checked={indexSources.includes(fid(source))} disabled={busy||(!indexSources.includes(fid(source))&&indexSources.length>=500)} onChange={event=>setIndexSources(current=>event.target.checked?[...current,fid(source)]:current.filter(id=>id!==fid(source)))}/><span>{source.name}</span></label>)}</details><Button size="sm" variant="secondary" isDisabled={protectedAction} onPress={()=>void indexCurrent()}>{indexSources.length?t('用所选资料构建集合','Build selected collection')+' ('+indexSources.length+')':t('用此文件构建检索集合','Build collection from this file')}</Button>{collection&&<p className="fw-muted">{t('集合修订','Collection revision')} {collection.revision} · {collection.embedding_version}</p>}{collection&&props.onUseCollection&&<Button size="sm" variant="secondary" isDisabled={busy} onPress={()=>props.onUseCollection?.(collection)}>{t('在下一条任务使用此集合','Use collection in next task')}</Button>}<p className="fw-muted">{t('发送任务时固定集合版本；不会把整个集合当作原件附件传入模型。','The collection version is pinned when you send. Its files are not added as raw model attachments.')}</p><div className="fw-form-row"><label>{t('检索问题','Search question')}<Input value={knowledgeQuery} onChange={event=>setKnowledgeQuery(event.target.value)} placeholder={t('例如：退款需要满足哪些条件？','For example: what are the refund conditions?')}/></label><Button size="sm" variant="primary" isDisabled={busy||!knowledgeQuery.trim()} onPress={()=>void searchKnowledge()}>{t('检索','Search')}</Button></div>{knowledgeHits.map(hit=><article className="fw-evidence" key={hit.id}><p>{hit.text}</p><small>{hit.version_id} · {hit.representation_id}</small><details><summary>{t('原文位置','Source location')}</summary><pre>{printable(hit.anchor)}</pre></details></article>)}</section>
      <details className="fw-disclosure"><summary>{t('提取归档中的文件','Extract archive members')}</summary><label className="fw-field">{t('成员路径，每行一项','Member paths, one per line')}<TextArea value={member} onChange={event=>setMember(event.target.value)} rows={3}/></label><Button size="sm" variant="secondary" isDisabled={protectedAction||!member.trim()} onPress={()=>void start('extract')}>{t('提取所选文件','Extract selected files')}</Button></details></Tabs.Panel>
     <Tabs.Panel id="data"><section className="fw-tool-section"><div className="fw-section-heading"><h4>{t('查询数据','Query data')}</h4><span>{t('只读 SQL · data 表','Read-only SQL · data table')}</span></div><TextArea aria-label="SQL" className="fw-code-input" value={sql} onChange={event=>setSQL(event.target.value)} rows={4}/><div className="fw-action-row"><Button size="sm" variant="primary" isDisabled={protectedAction||!sql.trim()} onPress={()=>void start('query',{sql,parameters:[],max_rows:1000})}>{t('运行查询','Run query')}</Button><Button size="sm" variant="ghost" isDisabled={writeBlocked} onPress={()=>void start('recalculate')}>{t('重算工作簿','Recalculate workbook')}</Button></div>{resultArea}{result.rows&&<><p className="fw-result-count">{t('返回行数','Returned rows')} {result.rows.length}</p><DataTable rows={result.rows} columns={result.columns||[]}/></>}</section>
      <section className="fw-tool-section">{details.permissions?.write!==true&&<p className="fw-muted">{t('此文件当前为只读，仍可读取、检索和查询数据。','This file is read-only. Reading, search and data queries remain available.')}</p>}<div className="fw-section-heading"><h4>{t('准备文件修改','Prepare an edit')}</h4><span>L1</span></div><p className="fw-muted">{t('修改生成候选版本。验证通过后，才可发布为当前版本。','Edits produce a candidate. Publish it only after verification succeeds.')}</p>{!advancedEdit&&<div className="fw-edit-form">{/spreadsheet|excel/.test(mime)?<><div className="fw-form-row"><label>{t('单元格','Cell')}<Input value={cell} onChange={event=>setCell(event.target.value.toUpperCase())} placeholder="B2"/></label><label>{t('内容类型','Value type')}<select value={editMode} onChange={event=>setEditMode(event.target.value)}><option value="value">{t('文本','Text')}</option><option value="number">{t('数值','Number')}</option><option value="formula">{t('公式','Formula')}</option></select></label></div><label className="fw-field">{editMode==='formula'?t('新公式','New formula'):t('新值','New value')}<Input value={cellValue} onChange={event=>setCellValue(event.target.value)} placeholder={editMode==='formula'?'=SUM(B2:B10)':''}/></label></>:/wordprocessingml|presentationml/.test(mime)?<><div className="fw-form-row">{/wordprocessingml/.test(mime)?<label>{t('段落序号（从 0 开始）','Paragraph index (from 0)')}<Input type="number" min="0" value={paragraph} onChange={event=>setParagraph(event.target.value)}/></label>:<><label>{t('幻灯片序号（从 0 开始）','Slide index (from 0)')}<Input type="number" min="0" value={slide} onChange={event=>setSlide(event.target.value)}/></label><label>{t('形状 ID','Shape ID')}<Input value={shape} onChange={event=>setShape(event.target.value)}/></label></>}</div><label className="fw-field">{t('原文','Expected text')}<TextArea rows={2} value={oldText} onChange={event=>setOldText(event.target.value)}/></label><label className="fw-field">{t('替换为','Replacement text')}<TextArea rows={3} value={newText} onChange={event=>setNewText(event.target.value)}/></label></>:<p className="fw-muted">{t('该格式的编辑需使用高级操作，或先转换为可编辑格式。','Use advanced operations or convert this source into an editable format.')}</p>}</div>}<details className="fw-disclosure"><summary>{t('高级编辑操作','Advanced edit operations')}</summary><label className="fw-checkbox"><input type="checkbox" checked={advancedEdit} onChange={event=>setAdvancedEdit(event.target.checked)}/>{t('使用 JSON 操作代替上方表单','Use JSON operations instead of the form')}</label><p className="fw-muted">{t('表格示例：','Spreadsheet example:')} <code>{'{"type":"set_cell","sheet":"Sheet1","cell":"B2","value":42}'}</code></p><TextArea aria-label={t('编辑操作 JSON','Edit operations JSON')} className="fw-code-input" value={editJSON} onChange={event=>setEditJSON(event.target.value)} rows={6}/></details><Button size="sm" variant="secondary" isDisabled={writeBlocked} onPress={()=>void start('edit')}>{t('生成候选版本','Create candidate')}</Button>{result.verification!=null&&<JsonView value={result.verification} label={t('验证结果','Verification')}/>}</section>
     </Tabs.Panel>
     <Tabs.Panel id="versions"><div className="fw-version-list">{details.versions.map(version=><div key={version.version_id||version.id}><div><strong>{(version.version_id||version.id)===details.file.head_version_id?t('当前版本','Current version'):t('历史版本','Previous version')}</strong><small>{dateText(version.created_at,zh)} · {size(version.size_bytes)}</small><code>{version.sha256}</code></div><a href={download(version.version_id||version.id!)} download>{t('下载','Download')}</a></div>)}</div>
      <section className="fw-tool-section"><h4>{t('待发布修改','Candidate changes')}</h4>{details.changesets.length?details.changesets.map(change=><div className="fw-change" key={change.changeset_id}><div><strong>{change.status}</strong><code>{change.changeset_id}</code><small>{change.required_fidelity||'L1'} · {change.published_version||change.candidate_sha256}</small></div><Button size="sm" variant="primary" isDisabled={writeBlocked||change.status!=='verified'||!change.verification_digest||!!change.published_version||change.expected_head!==details.file.head_version_id} onPress={()=>void guard(async()=>{const serial=epoch.current;await api('/files/'+encodeURIComponent(selectedFile)+'/publish','POST',{changeset_id:change.changeset_id,expected_head:change.expected_head,verification_digest:change.verification_digest});if(serial!==epoch.current||!alive.current)return;await refresh();changed();setNotice(t('新版本已发布','New version published'));})}>{t('发布新版本','Publish version')}</Button>{change.expected_head!==details.file.head_version_id&&!change.published_version&&<p className="fw-muted">{t('当前版本已变化，请重新准备修改。','The current version changed. Prepare a new candidate.')}</p>}</div>):<p className="fw-muted">{t('没有待发布的修改。','No candidate changes.')}</p>}</section>
      <details className="fw-disclosure"><summary>{t('访问权限','Access permissions')}</summary>{details.grants.map(grant=><div className="fw-grant" key={grant.subject_id+grant.permission}><span>{grant.subject_id}</span><small>{grant.permission}</small><Button variant="ghost" size="sm" isDisabled={manageBlocked} onPress={()=>void action('revoke',{subject_id:grant.subject_id})}>{t('撤销','Revoke')}</Button></div>)}<div className="fw-form-row"><label>{t('用户 ID','User ID')}<Input value={subject} onChange={event=>setSubject(event.target.value)}/></label><label>{t('权限','Permission')}<select value={permission} onChange={event=>setPermission(event.target.value)}><option value="read">{t('读取','Read')}</option><option value="write">{t('编辑','Write')}</option></select></label><Button size="sm" variant="secondary" isDisabled={manageBlocked||!subject.trim()} onPress={()=>void action('grant',{subject_id:subject.trim(),permission})}>{t('授权','Grant access')}</Button></div></details>
      <details className="fw-disclosure"><summary>{t('文件管理','Manage file')}</summary><div className="fw-form-row"><label>{t('文件名','File name')}<Input value={rename} onChange={event=>setRename(event.target.value)}/></label><Button size="sm" variant="secondary" isDisabled={manageBlocked||!rename.trim()} onPress={()=>void action('rename',{name:rename.trim()})}>{t('重命名','Rename')}</Button><Button size="sm" variant="danger-soft" isDisabled={manageBlocked} onPress={()=>void action('trash')}>{t('移入回收站','Move to trash')}</Button></div></details>
     </Tabs.Panel>
    </Tabs>
    {details.jobs.length>0&&<details className="fw-disclosure fw-history"><summary>{t('处理记录','Processing history')} <span>{details.jobs.length}</span></summary>{details.jobs.map(entry=><Button className="fw-job-history" variant="ghost" key={entry.id} onPress={()=>{setJob({...entry,result:unpack(entry.result)});setTab(entry.operation==='query'?'data':'structure');}}><span>{entry.operation}</span><span>{jobLabel(entry.status)}</span><small>{dateText(entry.created_at,zh)}</small></Button>)}</details>}
    {job?.result!=null&&<details className="fw-disclosure fw-raw"><summary>{t('完整处理结果','Full processing result')}</summary><JsonView value={result} label="JSON"/></details>}
   </>}
  </main></div>
 </div>;
}

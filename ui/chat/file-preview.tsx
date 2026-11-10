import React,{useEffect,useRef,useState} from 'react';
import {Button,Input} from '@heroui/react';
import {CodeBlock} from '@heroui-pro/react/code-block';
import type {FilePanelProps,FileArtifact,FileBlock,FileEvidence,FileKind,FileSource,SourceAnchor} from './file-types';
import {anchorLabel,byteLabel} from './file-types';
import './file-preview.css';

type Box=[number,number,number,number];
const FULL:Box=[0,0,1,1];
const finite=(value:unknown):value is number=>typeof value==='number'&&Number.isFinite(value);
const boxOf=(value:unknown):Box|undefined=>Array.isArray(value)&&value.length===4&&value.every(finite)&&value.every(x=>x>=0&&x<=1)&&value[0]<value[2]&&value[1]<value[3]?value as Box:undefined;
const textOf=(value:unknown)=>typeof value==='string'?value:value==null?'':JSON.stringify(value,null,2);
function localURL(value?:string){if(!value)return undefined;try{const u=new URL(value,location.href);return u.origin===location.origin&&['http:','https:'].includes(u.protocol)?u.href:undefined;}catch{return undefined;}}
function asIndices(value:unknown):number[]{return Array.isArray(value)?value.filter((n):n is number=>Number.isInteger(n)&&n>=0):[];}
function rangeText(selector:SourceAnchor,singular:string,plural:string){const values=finite(selector[singular])?[selector[singular] as number]:asIndices(selector[plural]);return values.map(n=>n+1).join(',');}
function parseRanges(value:string,max:number,zh:boolean):number[]{
 const values=new Set<number>();if(!value.trim())return [];
 for(const raw of value.split(/[,，]/)){const m=/^\s*(\d+)(?:\s*-\s*(\d+))?\s*$/.exec(raw);if(!m)throw Error(zh?'范围格式为 1-3,5':'Use ranges such as 1-3,5');const a=Number(m[1]),b=Number(m[2]||m[1]);if(a<1||b<a||b-a>=max||b>1000000)throw Error(zh?`单次最多选择 ${max} 项`:`Select at most ${max} items`);for(let n=a;n<=b;n++)values.add(n-1);if(values.size>max)throw Error(zh?`单次最多选择 ${max} 项`:`Select at most ${max} items`);}
 return [...values].sort((a,b)=>a-b);
}
function whole(value:string,min:number,max:number,zh:boolean){const n=Number(value);if(!value.trim()||!Number.isInteger(n)||n<min||n>max)throw Error(zh?`请输入 ${min}–${max} 之间的整数`:`Enter a whole number between ${min} and ${max}`);return n;}

/** Interpret an actual provenance anchor as a new bounded read selection. */
export function anchorSelector(anchor:SourceAnchor,kind:FileKind):SourceAnchor{
 if(kind==='xlsx'&&typeof anchor.sheet==='string')return {sheet:anchor.sheet,...(typeof anchor.range==='string'?{range:anchor.range}:{})};
 if(kind==='pptx'&&finite(anchor.slide_index))return {slide_index:anchor.slide_index,...(finite(anchor.shape_id)?{shape_id:anchor.shape_id}:{})};
 if(finite(anchor.page_index))return {page_index:anchor.page_index,...(boxOf(anchor.bbox)?{bbox:anchor.bbox}:{})};
 if(kind==='image')return boxOf(anchor.bbox)?{bbox:anchor.bbox}:{};
 if(kind==='docx'&&anchor.part&&anchor.part!=='body')return {};
 if(kind==='docx'&&finite(anchor.table_index))return {table_index:anchor.table_index};
 if(kind==='docx'&&finite(anchor.paragraph_index))return {paragraph_index:anchor.paragraph_index};
 if(kind==='docx'&&finite(anchor.block_index))return {block_index:anchor.block_index};
 if(kind==='notebook'&&finite(anchor.cell_index))return {cell_index:anchor.cell_index};
 if(kind==='archive'&&typeof anchor.member==='string')return {members:[anchor.member]};
 if(kind==='table'&&finite(anchor.row_index))return {start:anchor.row_index,count:1};
 if(finite(anchor.start_line)||finite(anchor.line))return {start_line:anchor.start_line||anchor.line,max_lines:20};
 return {};
}

export type RegionPickerProps={src:string;alt:string;value?:Box;onChange:(value?:Box)=>void;zh:boolean;disabled?:boolean;highlight?:Box};
export function RegionPicker({src,alt,value,onChange,zh,disabled=false,highlight}:RegionPickerProps){
 const [selecting,setSelecting]=useState(false),[drag,setDrag]=useState<Box>(),[failed,setFailed]=useState(false),[coordinates,setCoordinates]=useState(['0','0','100','100']),[error,setError]=useState('');
 const start=useRef<[number,number]|undefined>(undefined),image=useRef<HTMLImageElement>(null),pointer=useRef<number|undefined>(undefined);
 useEffect(()=>{setFailed(false);setSelecting(false);setDrag(undefined);start.current=undefined;},[src]);
 useEffect(()=>setCoordinates((value||FULL).map(v=>String(Math.round(v*10000)/100))),[value?.join(',')]);
 const t=(cn:string,en:string)=>zh?cn:en;
 const point=(event:React.PointerEvent)=>{const r=image.current?.getBoundingClientRect();if(!r?.width||!r.height)return undefined;return [Math.max(0,Math.min(1,(event.clientX-r.left)/r.width)),Math.max(0,Math.min(1,(event.clientY-r.top)/r.height))] as [number,number];};
 const move=(event:React.PointerEvent)=>{if(!start.current||pointer.current!==event.pointerId)return;const p=point(event);if(p)setDrag([Math.min(start.current[0],p[0]),Math.min(start.current[1],p[1]),Math.max(start.current[0],p[0]),Math.max(start.current[1],p[1])]);};
 const finish=(event:React.PointerEvent)=>{if(!start.current||pointer.current!==event.pointerId)return;const p=point(event),a=start.current;start.current=undefined;pointer.current=undefined;setDrag(undefined);if(event.currentTarget.hasPointerCapture(event.pointerId))event.currentTarget.releasePointerCapture(event.pointerId);if(p){const box:Box=[Math.min(a[0],p[0]),Math.min(a[1],p[1]),Math.max(a[0],p[0]),Math.max(a[1],p[1])];if(box[2]-box[0]>=.002&&box[3]-box[1]>=.002){onChange(box);setSelecting(false);}else setError(t('请拖出一个可见区域','Drag a visible region'));}};
 const rect=drag||value||highlight;
 const applyCoordinates=()=>{const parsed=coordinates.map(n=>Number(n)/100),valid=boxOf(parsed);if(coordinates.some(n=>!n.trim())||!valid){setError(t('百分比需在 0–100 之间，右下角应位于左上角之后','Use 0–100%; bottom-right must follow top-left'));return;}setError('');onChange(valid);};
 return <div className="fp-region" data-region-picker>
  <div className="fp-region-tools"><Button variant={selecting?'secondary':'ghost'} size="sm" isDisabled={disabled||failed} onPress={()=>{setSelecting(v=>!v);setError('');}}>{selecting?t('拖动框选区域','Drag to select a region'):t('选择图片区域','Select image region')}</Button>{value&&<Button variant="ghost" size="sm" isDisabled={disabled} onPress={()=>{onChange(undefined);setError('');}}>{t('恢复整页','Use full page')}</Button>}<span>{value?t('仅处理所选区域','Only the selected region will be processed'):t('点击按钮后拖动，或输入范围','Drag after selecting, or enter coordinates')}</span></div>
  {failed?<div className="fp-empty" role="status">{t('图片预览未能加载，可重新生成预览或下载原件。','Image preview could not load. Render again or download the source.')}</div>:<div className="fp-image-wrap"><div className="fp-image-canvas" data-selecting={selecting} role="group" aria-label={t('图片区域选择','Image region selection')}
   onPointerDown={event=>{if(disabled||!selecting||event.button!==0)return;const p=point(event);if(!p)return;event.preventDefault();pointer.current=event.pointerId;start.current=p;setDrag([p[0],p[1],p[0],p[1]]);event.currentTarget.setPointerCapture(event.pointerId);}}
   onPointerMove={move} onPointerUp={finish} onPointerCancel={()=>{start.current=undefined;pointer.current=undefined;setDrag(undefined);}}>
   <img ref={image} src={src} alt={alt} draggable={false} onError={()=>setFailed(true)} referrerPolicy="no-referrer"/>
   {rect&&<svg className="fp-region-overlay" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true"><rect x={rect[0]*100} y={rect[1]*100} width={(rect[2]-rect[0])*100} height={(rect[3]-rect[1])*100}/></svg>}
  </div></div>}
  <details className="fp-coordinates"><summary>{t('精确区域（百分比）','Exact region (%)')}</summary><div className="fp-fields">{[t('左','Left'),t('上','Top'),t('右','Right'),t('下','Bottom')].map((label,i)=><label key={label}>{label}<Input type="number" min={0} max={100} step="0.1" aria-label={label+' %'} value={coordinates[i]} disabled={disabled} onChange={event=>setCoordinates(values=>values.map((v,j)=>i===j?event.target.value:v))}/></label>)}<Button variant="secondary" size="sm" isDisabled={disabled} onPress={applyCoordinates}>{t('应用范围','Apply region')}</Button></div><p>{t('坐标以图片左上角为原点，整页为 0, 0, 100, 100。','Coordinates begin at the top-left; the full image is 0, 0, 100, 100.')}</p></details>
  {error&&<p className="fp-error" role="alert">{error}</p>}
 </div>;
}

function artifactPage(artifact:FileArtifact){const a=artifact.selector||{};return finite(a.slide_index)?a.slide_index:finite(a.page_index)?a.page_index:undefined;}
function artifactName(artifact:FileArtifact,zh:boolean){const anchor=artifact.selector||{};return finite(anchor.slide_index)?(zh?'幻灯片 ':'Slide ')+(anchor.slide_index+1):finite(anchor.page_index)?(zh?'第 ':'Page ')+(anchor.page_index+1)+(zh?' 页':''):artifact.name||(zh?'图片预览':'Image preview');}
function PreviewSelection({props,onChange}:{props:FilePanelProps;onChange:(selector:SourceAnchor)=>void}){
 const {kind,selector,zh,disabled}=props,t=(a:string,b:string)=>zh?a:b;
 const renderDisabled=disabled||(kind==='pdf'?props.result.capabilities?.pdf_render===false:props.result.capabilities?.office_render===false);
 const [range,setRange]=useState(''),[error,setError]=useState('');
 useEffect(()=>{setRange(rangeText(selector,kind==='pptx'?'slide_index':'page_index',kind==='pptx'?'slides':'pages'));setError('');},[kind,JSON.stringify(selector)]);
 if(!['pdf','pptx','docx','xlsx'].includes(kind))return null;
 return <div className="fp-page-selection"><label>{kind==='pptx'?t('幻灯片范围','Slides'):t('预览页码','Preview pages')}<Input aria-label={kind==='pptx'?t('幻灯片范围','Slides'):t('预览页码','Preview pages')} value={range} placeholder={t('留空读取前 10 页','Leave empty for the first 10 pages')} disabled={disabled} onChange={event=>setRange(event.target.value)}/></label><Button variant="secondary" size="sm" isDisabled={renderDisabled} onPress={()=>{try{const indices=parseRanges(range,10,zh);const selection=indices.length?{[kind==='pptx'?'slides':'pages']:indices}:{};onChange(selection);void props.onRun({operation:'render',selector:selection});setError('');}catch(ex){setError((ex as Error).message);}}}>{t('生成预览','Render preview')}</Button><small>{t('页码从 1 开始，单次最多 10 页','Pages start at 1, with up to 10 per operation')}</small>{renderDisabled&&!disabled&&<p className="fp-caption">{t('当前处理环境未配置此格式的渲染器，仍可读取结构或下载原件。','The processing environment has no renderer for this format. You can still read its structure or download the source.')}</p>}{error&&<p className="fp-error" role="alert">{error}</p>}</div>;
}

export function PreviewPanel(props:FilePanelProps){
 const {kind,source,version,result,job,zh,disabled,selector,onSelectorChange,onRun,artifactURL}=props,t=(a:string,b:string)=>zh?a:b;
 const [active,setActive]=useState(0),[region,setRegion]=useState<Box>();
 const images=(result.artifacts||[]).filter(a=>/^image\/(png|jpeg|webp)$/i.test(a.mime_type||''));
 const pdfs=(result.artifacts||[]).filter(a=>a.mime_type==='application/pdf');
 const selected=images[Math.min(active,images.length-1)];
 const fullOriginal=kind==='image'?localURL(props.downloadURL(version.version_id||version.id)):undefined;
 const selectedURL=selected?localURL(artifactURL(selected)):undefined;
 const imageURL=kind==='image'?fullOriginal:selectedURL;
 const targetPage=selected?artifactPage(selected):undefined;
 const sourceBox=boxOf(selected?.selector?.bbox);
 const alreadyCropped=kind!=='image'&&!!sourceBox&&sourceBox.some((n,i)=>n!==FULL[i]);
 useEffect(()=>{setActive(0);},[job?.id,source.id,version.version_id]);
 useEffect(()=>{
  const page=finite(selector.slide_index)?selector.slide_index:finite(selector.page_index)?selector.page_index:undefined;
  if(page!==undefined){const match=images.findIndex(a=>artifactPage(a)===page);if(match>=0)setActive(match);}
  setRegion(boxOf(selector.bbox));
 },[JSON.stringify(selector),job?.id]);
 const choose=(index:number)=>{setActive(index);setRegion(undefined);const page=artifactPage(images[index]);onSelectorChange(page==null?{}:kind==='pptx'?{slide_index:page}:{page_index:page});};
 const selectedRegion=(box?:Box)=>{setRegion(box);const value:SourceAnchor=kind==='image'?{}:targetPage==null?{}:kind==='pptx'?{slide_index:targetPage}:{page_index:targetPage};if(box)value.bbox=box;onSelectorChange(value);};
 const runRegion=(ocr=false)=>{const value:SourceAnchor=kind==='image'?{}:targetPage==null?{}:{page_index:targetPage};if(region)value.bbox=region;onSelectorChange(value);void onRun({operation:ocr?'read':'render',selector:value,options:ocr?{ocr:true}:{}});};
 const canRegion=kind==='image'||kind==='pdf';
 return <section className="fp-preview" aria-label={t('文件预览','File preview')}>
  <PreviewSelection props={props} onChange={onSelectorChange}/>
  {kind==='image'&&<p className="fp-caption">{t('原件保留不变。框选后可生成独立裁剪图，或识别区域中的文字。','The original is preserved. Select a region to create a separate crop or recognize its text.')}</p>}
  {images.length>1&&<nav className="fp-thumbnails" aria-label={t('预览页导航','Preview page navigation')}>{images.map((a,i)=><Button variant="ghost" key={a.id||a.artifact_id||i} className="fp-thumbnail" aria-pressed={i===active} onPress={()=>choose(i)}><img src={localURL(artifactURL(a))} alt="" loading="lazy"/><span>{artifactName(a,zh)}</span></Button>)}</nav>}
  {imageURL?<>
   <div className="fp-preview-title"><strong>{kind==='image'?source.name:selected?artifactName(selected,zh):source.name}</strong>{selectedURL&&<a href={selectedURL} download={selected?.name}>{t('下载此图','Download image')}</a>}</div>
   {canRegion&&!alreadyCropped?<RegionPicker key={imageURL} src={imageURL} alt={kind==='image'?source.name:selected?artifactName(selected,zh):source.name} value={region} onChange={selectedRegion} zh={zh} disabled={disabled}/>:<div className="fp-image-wrap"><img className="fp-static-image" src={imageURL} alt={selected?artifactName(selected,zh):source.name}/></div>}
   {canRegion&&!alreadyCropped&&<div className="fp-actions"><Button variant="primary" size="sm" isDisabled={disabled||!region} onPress={()=>runRegion()}>{t('生成区域图片','Create region image')}</Button><Button variant="secondary" size="sm" isDisabled={disabled||result.capabilities?.ocr===false} onPress={()=>runRegion(true)}>{region?t('识别区域文字','Read region text'):t('识别本页文字','Read image text')}</Button></div>}
   {canRegion&&result.capabilities?.ocr===false&&<p className="fp-caption">{t('当前处理环境未配置 OCR；图片仍可预览和裁剪。','OCR is not configured in this processing environment. Preview and crop remain available.')}</p>}
   {alreadyCropped&&<p className="fp-caption">{t('这是已生成的局部图。需要重新选区时，请先生成整页预览。','This is a previously cropped image. Render the full page before choosing another region.')}</p>}
  </>:!images.length&&<div className="fp-empty"><strong>{t('尚未生成页面预览','No rendered preview yet')}</strong><p>{['pdf','docx','xlsx','pptx'].includes(kind)?t('选择页码并生成预览，结果会保留文件版本和页面位置。','Choose pages and render. Results keep their file version and page locations.'):t('此类型可在结构视图读取内容，或下载原件。','Read this file in the structure view, or download the original.')}</p>{kind==='image'&&<Button variant="secondary" size="sm" isDisabled={disabled} onPress={()=>void onRun({operation:'render',selector:{}})}>{t('生成图片预览','Render image')}</Button>}</div>}
  {kind==='image'&&images.length>0&&<section className="fp-derived-images"><h4>{t('生成的图片','Generated images')}</h4>{images.map((a,i)=><figure key={a.id||i}><img src={localURL(artifactURL(a))} alt={artifactName(a,zh)} loading="lazy"/><figcaption><a href={localURL(artifactURL(a))} download={a.name}>{a.name||t('下载图片','Download image')}</a><span>{byteLabel(a.size_bytes)}</span></figcaption></figure>)}</section>}
  {pdfs.length>0&&<div className="fp-document-downloads">{pdfs.map((a,i)=><a key={a.id||i} href={localURL(artifactURL(a))} download={a.name}>{t('下载完整渲染 PDF','Download rendered PDF')} <span>{byteLabel(a.size_bytes)}</span></a>)}</div>}
  {pdfs.length>0&&!images.length&&<p className="fp-caption">{t('本次结果只包含 PDF。可下载查看，或重新生成选定页的图片。','This result contains a PDF only. Download it or render selected pages as images.')}</p>}
 </section>;
}

function BlockView({block,zh,onLocate}:{block:FileBlock;zh:boolean;onLocate?:(anchor:SourceAnchor)=>void}){
 const low=block.low_confidence===true,anchor=block.anchor||{},kind=block.kind||'text';
 const headings:Record<string,[string,string]>={paragraph:['段落','Paragraph'],heading:['标题','Heading'],table:['表格','Table'],cell:['单元格','Cell'],formula_cell:['公式单元格','Formula'],notes:['备注','Notes'],header:['页眉','Header'],footer:['页脚','Footer'],image:['图片','Image'],ocr_text:['识别文字','Recognized text'],row:['数据行','Data row'],code:['代码','Code'],markdown:['Markdown','Markdown'],stored_output:['已保存的输出','Saved output']};
 return <article className="fp-block" data-block-id={block.id} data-low-confidence={low||undefined}>
  <header><span>{headings[kind]?.[zh?0:1]||(zh?'内容':'Content')}</span>{onLocate&&(!anchor.part||anchor.part==='body')?<Button variant="ghost" size="sm" className="fp-location" onPress={()=>onLocate(anchor)}>{anchorLabel(anchor,zh)}</Button>:<span className="fp-location">{anchor.part&&anchor.part!=='body'?(zh?'页眉 / 页脚 · 分节 ':'Header / footer · section ')+String(Number(anchor.section||0)+1):anchorLabel(anchor,zh)}</span>}</header>
  {low&&<p className="fp-low-confidence" role="status">{zh?'低置信识别，请对照原图核实':'Low confidence — verify against the original image'}{finite(block.confidence)?` · ${Math.round(block.confidence*100)}%`:''}</p>}
  {kind==='image'?<p className="fp-caption">{zh?'此处包含图片；结构读取不代表已理解图片内容。请打开对应页面预览。':'This location contains an image. Structured reading does not imply visual understanding; open its page preview.'}</p>:kind==='code'?<CodeBlock className="fp-code"><CodeBlock.Header>{zh?'代码单元（未执行）':'Code cell (not executed)'}</CodeBlock.Header><pre>{block.text||''}</pre></CodeBlock>:<p className="fp-block-text">{block.text||'—'}</p>}
  {(block.formula!=null||block.cached_value!=null)&&<dl className="fp-cell-values">{block.formula!=null&&<><dt>{zh?'公式':'Formula'}</dt><dd><code>{textOf(block.formula)}</code></dd></>}{block.cached_value!=null&&<><dt>{zh?'已保存的值':'Saved value'}</dt><dd>{textOf(block.cached_value)}</dd></>}<dt>{zh?'计算状态':'Calculation'}</dt><dd>{block.calculation_status==='recalculated'?(zh?'已重算':'Recalculated'):(zh?'读取缓存，未重新计算':'Saved cache; not recalculated')}</dd></dl>}
 </article>;
}

function NotebookExecutionResult({props}:{props:FilePanelProps}){
 const {zh,result,job}=props,t=(a:string,b:string)=>zh?a:b;
 const candidate=result.candidate||(result.artifacts||[]).find(a=>a.name?.endsWith('.ipynb'));
 const url=candidate?localURL(props.artifactURL(candidate)):undefined;
 const [cells,setCells]=useState<Array<{index:number;source:string;outputs:string[]}>>(),[error,setError]=useState(''),[loading,setLoading]=useState(false);
 const request=useRef<AbortController|undefined>(undefined);
 useEffect(()=>{setCells(undefined);setError('');setLoading(false);return()=>request.current?.abort();},[job?.id]);
 const load=async()=>{if(!url||loading)return;const controller=new AbortController();request.current=controller;setLoading(true);setError('');const timer=setTimeout(()=>{if(!controller.signal.aborted){setError(t('预览请求超时，可重试或下载完整文件','Preview request timed out. Retry or download the complete file.'));setLoading(false);controller.abort();}},30000);try{
  const response=await fetch(url,{credentials:'same-origin',signal:controller.signal});if(!response.ok)throw Error(t('执行产物暂时不可用','Execution artifact is unavailable'));if(Number(response.headers.get('Content-Length')||0)>(2<<20)){controller.abort();setError(t('结果超过 2 MiB 预览上限，请下载完整文件','Result exceeds the 2 MiB preview limit. Download the complete file.'));setLoading(false);return;}const reader=response.body?.getReader();if(!reader)throw Error(t('无法读取执行产物','Cannot read execution artifact'));
  const chunks:Uint8Array[]=[];let size=0;try{for(;;){const item=await reader.read();if(item.done)break;size+=item.value.length;if(size>(2<<20)){await reader.cancel();throw Error(t('结果超过 2 MiB 预览上限，请下载完整文件','Result exceeds the 2 MiB preview limit. Download the complete file.'));}chunks.push(item.value);}}finally{reader.releaseLock();}
  const data=new Uint8Array(size);let offset=0;for(const chunk of chunks){data.set(chunk,offset);offset+=chunk.length;}const notebook=JSON.parse(new TextDecoder().decode(data));if(notebook.nbformat!==4||!Array.isArray(notebook.cells))throw Error(t('执行产物不是有效笔记本','Execution artifact is not a valid notebook'));
  const selected=asIndices(job?.request?.selector?.cell_indices),indices=selected.length?selected:Array.from({length:Math.min(notebook.cells.length,100)},(_,i)=>i);
  const plain=(value:any)=>Array.isArray(value)?value.join(''):typeof value==='string'?value:'';
  const parsed=indices.filter(i=>i<notebook.cells.length).slice(0,100).map(index=>{const cell=notebook.cells[index];return {index,source:plain(cell.source).slice(0,12000),outputs:(Array.isArray(cell.outputs)?cell.outputs:[]).slice(0,20).map((o:any)=>plain(o.text||o.data?.['text/plain']||o.traceback)).filter(Boolean).map((text:string)=>text.slice(0,12000))};});
  if(!controller.signal.aborted)setCells(parsed);
 }catch(ex){if(!controller.signal.aborted)setError((ex as Error).message);}finally{clearTimeout(timer);if(!controller.signal.aborted)setLoading(false);}};
 return <section className="fp-execution-result"><h4>{t('执行结果','Execution result')}</h4><p className="fp-caption">{result.verification?.passed?t('所选单元格已执行并写入新候选文件；原件仍保留。','Selected cells were executed into a new candidate. The original is preserved.'):t('执行未通过完整验证，请查看错误和保留的输出。','Execution did not pass verification. Review its error and saved output.')}</p>{url&&<div className="fp-actions"><Button variant="secondary" size="sm" isPending={loading} isDisabled={loading} onPress={()=>void load()}>{t('查看保存的文本输出','View saved text outputs')}</Button><a href={url} download={candidate?.name}>{t('下载执行后的笔记本','Download executed notebook')}</a></div>}{error&&<p className="fp-error" role="alert">{error}</p>}{cells?.map(cell=><article key={cell.index} className="fp-notebook-cell"><h5>{t('单元格 ','Cell ')}{cell.index+1}</h5><CodeBlock className="fp-code"><CodeBlock.Header>{t('保存的代码','Saved code')}</CodeBlock.Header><pre>{cell.source}</pre></CodeBlock>{cell.outputs.length?cell.outputs.map((text,i)=><pre key={i} className="fp-saved-output">{text}</pre>):<p className="fp-caption">{t('此单元格没有保存的文本输出；图片或其它输出可在下载文件中查看。','This cell has no saved text output. Images or other outputs are available in the downloaded file.')}</p>}</article>)}</section>;
}

function Outline({props,onLocate}:{props:FilePanelProps;onLocate?:(anchor:SourceAnchor)=>void}){
 const items=props.result.outline||[],zh=props.zh;
 if(!items.length||props.kind==='archive')return null;
 const selectable=items.filter(item=>item.kind==='sheet'||item.kind==='slide'||finite(item.block_index)||finite(item.line));
 if(!selectable.length)return <div className="fp-inspection">{items.map((item,i)=><span key={i}>{finite(item.pages)?`${item.pages} ${zh?'页':'pages'}`:finite(item.width)&&finite(item.height)?`${item.width} × ${item.height} ${zh?'像素':'pixels'}`:item.kind===props.kind?(zh?'文件结构已检查':'File structure inspected'):item.kind==='structure'?`${zh?'结构':'Structure'} · ${item.type}${Array.isArray(item.keys)?' · '+item.keys.join(', '):''}`:item.kind==='table'?`${(item.columns||[]).length} ${zh?'列':'columns'} · ${item.rows} ${zh?'行':'rows'}`:null}</span>)}</div>;
 return <details className="fp-outline" open><summary>{zh?'目录与结构':'Contents and structure'}</summary><nav aria-label={zh?'文件结构':'Document structure'}>{selectable.map((item,i)=>{
  const anchor:SourceAnchor=item.kind==='sheet'?{kind:'sheet_range',sheet:item.name,sheet_id:item.sheet_id}:item.kind==='slide'?{kind:'slide_shape',slide_index:item.slide_index}:finite(item.line)?{kind:'text_range',start_line:item.line}:{kind:'document_block',block_index:item.block_index,...(finite(item.paragraph_index)?{paragraph_index:item.paragraph_index}:{})};
  const title=item.kind==='sheet'?item.name:item.kind==='slide'?`${Number(item.slide_index)+1}. ${item.title||(zh?'未命名幻灯片':'Untitled slide')}`:item.text||(zh?'段落':'Paragraph');
  return <Button key={i} variant="ghost" size="sm" className="fp-outline-item" isDisabled={props.disabled} onPress={()=>{props.onSelectorChange(anchorSelector(anchor,props.kind));onLocate?.(anchor);}}><span>{title}</span>{item.kind==='sheet'&&<small>{item.rows} × {item.columns}{item.state==='hidden'?(zh?' · 隐藏':' · hidden'):''}</small>}</Button>;
 })}</nav></details>;
}

export function StructurePanel(props:FilePanelProps&{onLocate?:(anchor:SourceAnchor)=>void}){
 const {kind,zh,result,disabled,selector,onSelectorChange,onRun,onLocate,canWrite}=props,t=(cn:string,en:string)=>zh?cn:en;
 const [documentMode,setDocumentMode]=useState('blocks');
 const [range,setRange]=useState(''),[sheet,setSheet]=useState(''),[cellRange,setCellRange]=useState(''),[start,setStart]=useState('1'),[count,setCount]=useState('100'),[shape,setShape]=useState(''),[layout,setLayout]=useState('simple'),[ocr,setOCR]=useState(true),[encoding,setEncoding]=useState('utf-8-sig'),[members,setMembers]=useState<string[]>([]),[cells,setCells]=useState<number[]>([]),[executeAgreed,setExecuteAgreed]=useState(false),[timeout,setTimeoutValue]=useState('30'),[error,setError]=useState('');
 useEffect(()=>{setRange(rangeText(selector,kind==='notebook'?'cell_index':kind==='pptx'?'slide_index':'page_index',kind==='notebook'?'cells':kind==='pptx'?'slides':'pages'));setSheet(typeof selector.sheet==='string'?selector.sheet:'');setCellRange(typeof selector.range==='string'?selector.range:'');setDocumentMode(finite(selector.paragraph_index)?'paragraph':finite(selector.table_index)?'table':'blocks');setStart(String((finite(selector.paragraph_index)?selector.paragraph_index+1:finite(selector.table_index)?selector.table_index+1:finite(selector.block_index)?selector.block_index+1:finite(selector.start)?selector.start+1:selector.start_line)||1));setCount(String(selector.max_lines||selector.count||100));setShape(selector.shape_id==null?'':String(selector.shape_id));},[JSON.stringify(selector),kind]);
 useEffect(()=>{setCells([]);setMembers([]);setExecuteAgreed(false);setError('');},[props.source.id,props.version.version_id,props.job?.id]);
 const makeSelection=():SourceAnchor=>{
  if(kind==='pdf'||kind==='pptx'||kind==='notebook'){const indices=parseRanges(range,kind==='notebook'?100:10,zh),s:SourceAnchor=indices.length?{[kind==='notebook'?'cells':kind==='pptx'?'slides':'pages']:indices}:{};if(kind==='pptx'&&shape.trim())s.shape_id=whole(shape,1,2147483647,zh);return s;}
  if(kind==='xlsx')return {...(sheet.trim()?{sheet:sheet.trim()}:{}),...(cellRange.trim()?{range:cellRange.trim()}: {})};
  if(kind==='docx'){const index=whole(start,1,1000000,zh)-1;return documentMode==='paragraph'?{paragraph_index:index}:documentMode==='table'?{table_index:index}:{start:index,count:whole(count,1,500,zh)};}
  if(kind==='table')return {start:whole(start,1,100000000,zh)-1,count:whole(count,1,500,zh)};
  if(kind==='text')return {start_line:whole(start,1,100000000,zh),max_lines:whole(count,1,2000,zh)};
  if(kind==='image')return boxOf(selector.bbox)?{bbox:selector.bbox}:{};
  return {};
 };
 const read=()=>{try{const selected=makeSelection();onSelectorChange(selected);setError('');void onRun({operation:kind==='archive'?'archive_list':'read',selector:selected,options:kind==='pdf'?{layout,ocr}:kind==='image'?{ocr:true}:['text','table'].includes(kind)?{encoding}:{}});}catch(ex){setError((ex as Error).message);}};
 const renderAnchor=(anchor:SourceAnchor)=>{onSelectorChange(anchorSelector(anchor,kind));onLocate?.(anchor);};
 const blocks=result.blocks||[],notebookCells=blocks.filter(b=>b.anchor?.kind==='notebook_cell'&&b.kind!=='stored_output'&&finite(b.anchor.cell_index));
 const archiveEntries=(result.outline||[]).filter(a=>typeof a.name==='string'&&typeof a.directory==='boolean');
 const execute=()=>{try{if(!canWrite||!executeAgreed||!cells.length)throw Error(t('选择代码单元并确认后才能执行','Select cells and confirm execution first'));const seconds=whole(timeout,1,120,zh);setError('');void onRun({operation:'notebook_execute',selector:{cell_indices:[...cells].sort((a,b)=>a-b)},options:{timeout_seconds:seconds,fidelity:'L1'}});}catch(ex){setError((ex as Error).message);}};
 return <section className="fp-structure" aria-label={t('文件结构和内容','File structure and content')}>
  <div className="fp-read-controls"><div className="fp-fields">
   {['pdf','pptx','notebook'].includes(kind)&&<label>{kind==='notebook'?t('单元格范围','Cell range'):kind==='pptx'?t('幻灯片范围','Slides'):t('页码范围','Pages')}<Input value={range} placeholder={kind==='notebook'?t('留空读取前 100 个单元格','First 100 cells by default'):t('留空读取前 10 页','First 10 pages by default')} disabled={disabled} onChange={e=>setRange(e.target.value)}/></label>}
   {kind==='pptx'&&<label>{t('形状 ID（可选）','Shape ID (optional)')}<Input value={shape} type="number" min={1} disabled={disabled} onChange={e=>setShape(e.target.value)}/></label>}
   {kind==='xlsx'&&<><label>{t('工作表','Worksheet')}<Input value={sheet} placeholder={t('留空使用首张工作表','First sheet by default')} disabled={disabled} onChange={e=>setSheet(e.target.value)}/></label><label>{t('单元格范围','Cell range')}<Input value={cellRange} placeholder="A1:F20" disabled={disabled} onChange={e=>setCellRange(e.target.value)}/></label></>}
   {kind==='docx'&&<label>{t('定位方式','Location type')}<select value={documentMode} disabled={disabled} onChange={e=>setDocumentMode(e.target.value)}><option value="blocks">{t('连续内容块','Consecutive blocks')}</option><option value="paragraph">{t('指定正文段落','Body paragraph')}</option><option value="table">{t('指定正文表格','Body table')}</option></select></label>}
   {['docx','table','text'].includes(kind)&&<><label>{kind==='docx'?(documentMode==='paragraph'?t('正文段落序号','Body paragraph number'):documentMode==='table'?t('正文表格序号','Body table number'):t('开始内容块','Start block')):t('开始行','Start row')}<Input type="number" min={1} value={start} disabled={disabled} onChange={e=>setStart(e.target.value)}/></label>{(kind!=='docx'||documentMode==='blocks')&&<label>{kind==='docx'?t('内容块数','Block count'):t('行数','Rows')}<Input type="number" min={1} max={kind==='text'?2000:500} value={count} disabled={disabled} onChange={e=>setCount(e.target.value)}/></label>}</>}
   {['text','table'].includes(kind)&&<label>{t('文本编码','Text encoding')}<select disabled={disabled} value={encoding} onChange={e=>setEncoding(e.target.value)}>{['utf-8-sig','utf-16','gb18030','latin-1'].map(value=><option key={value} value={value}>{value}</option>)}</select></label>}
   {kind==='pdf'&&<label>{t('读取方式','Extraction')}<select value={layout} disabled={disabled} onChange={e=>setLayout(e.target.value)}><option value="simple">{t('常规文字与扫描页','Text and scanned pages')}</option><option value="complex" disabled={result.capabilities?.complex_pdf===false}>{t('复杂排版与表格','Complex layout and tables')}</option></select></label>}
  </div><div className="fp-actions"><Button variant="primary" size="sm" isDisabled={disabled||kind==='unknown'} onPress={read}>{kind==='archive'?t('列出归档成员','List archive members'):t('读取选区','Read selection')}</Button>{kind==='pdf'&&<label className="fp-check"><input type="checkbox" checked={ocr} disabled={disabled} onChange={e=>setOCR(e.target.checked)}/>{t('扫描页使用 OCR','OCR for scanned pages')}</label>}</div><p className="fp-caption">{kind==='notebook'?t('读取只显示保存的代码和输出，不会执行单元格。','Reading displays saved code and output without executing cells.'):kind==='archive'?t('先检查清单，再选择需要提取的文件；目录和链接不会执行。','Inspect the listing, then select files to extract. Directories and links are not executed.'):kind==='xlsx'?t('公式与保存的缓存值分开显示；读取不会重算工作簿。','Formulas and saved cached values are shown separately. Reading does not recalculate.'):t('所有序号从 1 开始。仅显示实际读取的内容，未选部分不会视为已读。','Positions start at 1. Only returned selections are read; unselected content is not treated as read.')}</p></div>
  {error&&<p role="alert" className="fp-error">{error}</p>}
  <Outline props={props} onLocate={onLocate}/>
  {kind==='notebook'&&props.job?.operation==='notebook_execute'&&<NotebookExecutionResult props={props}/>}
  {kind==='archive'&&archiveEntries.length>0?<><div className="fp-archive-list">{archiveEntries.map(item=><label key={item.name} className="fp-archive-member"><input type="checkbox" checked={members.includes(item.name)} disabled={disabled||item.directory||(!members.includes(item.name)&&members.length>=100)} onChange={e=>setMembers(previous=>e.target.checked?[...previous,item.name]:previous.filter(p=>p!==item.name))}/><span>{item.name}{item.directory?'/':''}</span><small>{item.directory?t('目录','Folder'):byteLabel(item.size_bytes)}</small></label>)}</div><div className="fp-actions"><Button variant="secondary" size="sm" isDisabled={disabled||!members.length} onPress={()=>void onRun({operation:'extract',selector:{members}})}>{t(`提取所选 ${members.length} 个文件`,`Extract ${members.length} selected files`)}</Button><small>{t('单次最多 100 个成员','Up to 100 members per extraction')}</small></div></>:kind==='notebook'&&notebookCells.length>0?<>
   <div className="fp-notebook">{notebookCells.map(block=>{const cell=block.anchor!.cell_index as number;return <section key={block.id} className="fp-notebook-cell"><div className="fp-cell-select"><label className="fp-check"><input type="checkbox" checked={cells.includes(cell)} disabled={disabled||!canWrite||block.kind!=='code'} onChange={event=>setCells(previous=>event.target.checked?[...previous,cell]:previous.filter(n=>n!==cell))}/><strong>{t('单元格 ','Cell ')}{cell+1}</strong><small>{block.kind}</small></label></div><BlockView block={block} zh={zh} onLocate={onLocate}/>{blocks.filter(output=>output.kind==='stored_output'&&output.anchor?.cell_index===cell).map(output=><BlockView key={output.id} block={output} zh={zh}/>)}{(result.artifacts||[]).filter(a=>a.selector?.cell_index===cell).map((a,i)=><a className="fp-notebook-image" key={a.id||i} href={localURL(props.artifactURL(a))} download={a.name}><img src={localURL(props.artifactURL(a))} alt={t('已保存的单元格图片输出','Saved cell image output')}/></a>)}</section>;})}</div>
   <section className="fp-execute"><h4>{t('执行选定的代码单元','Run selected code cells')}</h4><p>{t('代码将在禁网的隔离环境中执行，产生可下载的新候选版本，原件不变。执行其他单元格依赖的代码前，请一并选择所需依赖。','Code runs in an isolated environment without network access and creates a downloadable candidate. The original is preserved. Include selected cells’ dependencies when needed.')}</p><label className="fp-check"><input type="checkbox" checked={executeAgreed} disabled={disabled||!canWrite} onChange={e=>setExecuteAgreed(e.target.checked)}/>{t('我确认执行所选代码','I confirm execution of the selected code')}</label><div className="fp-fields"><label>{t('执行时间上限（秒）','Execution limit (seconds)')}<Input type="number" min={1} max={120} value={timeout} disabled={disabled||!canWrite} onChange={e=>setTimeoutValue(e.target.value)}/></label><Button variant="primary" size="sm" isDisabled={disabled||!canWrite||!executeAgreed||!cells.length} onPress={execute}>{t(`执行所选单元格（${cells.length}）`,`Run selected cells (${cells.length})`)}</Button></div>{!canWrite&&<p className="fp-caption">{t('执行需要该文件的编辑权限。','Execution requires write access to this file.')}</p>}</section>
  </>:<div className="fp-block-list">{blocks.map(block=><BlockView key={block.id} block={block} zh={zh} onLocate={onLocate?renderAnchor:undefined}/>)}</div>}
  {!blocks.length&&props.job?.operation!=='notebook_execute'&&<div className="fp-empty"><strong>{t('还没有读取结果','No read result yet')}</strong><p>{t('选择需要的范围后读取。上传成功或文件检查完成不代表内容已经读取。','Choose a range and read it. Uploading or inspecting a file does not mean its content has been read.')}</p></div>}
  {kind==='archive'&&(result.artifacts||[]).length>0&&<div className="fp-extracted"><h4>{t('已提取文件','Extracted files')}</h4>{result.artifacts!.map((a,i)=><a key={a.id||i} href={localURL(props.artifactURL(a))} download={a.name}>{a.name||t('下载文件','Download file')} <span>{byteLabel(a.size_bytes)}</span></a>)}</div>}
 </section>;
}

export function EvidenceList({hits,zh,onLocate,busy=false,sources=[]}:{hits:FileEvidence[];zh:boolean;onLocate:(hit:FileEvidence)=>void;busy?:boolean;sources?:FileSource[]}){
 return <div className="fp-evidence-list">{hits.map((hit,index)=>{const source=sources.find(s=>s.version_id===hit.version_id);const title=source?.name||String(hit.source_name||hit.file_name||hit.title||(zh?'来源资料':'Source document'));return <article className="fp-evidence" key={hit.id||index}><div className="fp-evidence-head"><span>{index+1}</span><strong>{title}</strong><Button variant="ghost" size="sm" isDisabled={busy} onPress={()=>onLocate(hit)}>{zh?'定位原文':'Open source'}</Button></div><p>{hit.text||''}</p><footer>{anchorLabel(hit.anchor,zh)}{hit.low_confidence===true&&<span className="fp-low-confidence">{zh?'低置信识别，请核实原图':'Low confidence; verify original'}</span>}</footer></article>;})}</div>;
}

import type {ContextSource} from './context-panel';

export type FileKind='pdf'|'docx'|'xlsx'|'pptx'|'image'|'notebook'|'archive'|'table'|'text'|'unknown';
export type FileSource=ContextSource&{file_id?:string;version_id?:string;sha256?:string;selected_version?:boolean;version_label?:string};
export type FileVersion={version_id:string;id?:string;mime_type:string;size_bytes:number;sha256:string;created_at:string|number;created_by?:string;parent_version_id?:string};
export type SourceAnchor=Record<string,unknown>;
export type FileArtifact={id?:string;artifact_id?:string;name?:string;mime_type?:string;size_bytes?:number;sha256?:string;selector?:SourceAnchor};
export type FileBlock={id:string;kind?:string;text?:string;anchor?:SourceAnchor;confidence?:number;low_confidence?:boolean;[key:string]:unknown};
export type FileResult={status?:string;blocks?:FileBlock[];outline?:any[];coverage?:Record<string,any>;warnings?:any[];artifacts?:FileArtifact[];rows?:any[];columns?:any[];capabilities?:Record<string,any>;verification?:Record<string,any>;candidate?:FileArtifact;changeset_id?:string;[key:string]:any};
export type FileRoot={file_id:string;version_id:string;name?:string;sha256?:string;size_bytes?:number;acl_epoch?:number};
export type FileJob={id:string;operation:string;status:string;result?:FileResult;consumer_id?:string;inputs?:FileRoot[];request?:{selector?:SourceAnchor;options?:Record<string,any>};error?:unknown;created_at?:string|number};
export type FileChange={changeset_id:string;job_id?:string;expected_head:string;verification_digest?:string;status:string;candidate_sha256?:string;candidate_size?:number;required_fidelity?:string;published_version?:string;created_at?:number|string};
export type FileDetails={file:{file_id?:string;id?:string;name:string;head_version_id:string;created_by:string;lifecycle:string;acl_epoch?:number};versions:FileVersion[];grants:Array<{subject_id:string;permission:string}>;jobs:FileJob[];changesets:FileChange[];permissions?:{read:boolean;write:boolean;manage:boolean}};
export type RunRequest={operation:string;selector?:SourceAnchor;options?:Record<string,any>;inputs?:Array<{file_id:string;version_id?:string}>};
export type FileApi=(path:string,method?:string,body?:unknown,signal?:AbortSignal)=>Promise<any>;
export type FilePanelProps={kind:FileKind;source:FileSource;version:FileVersion;details:FileDetails;sources:FileSource[];result:FileResult;job:FileJob|null;zh:boolean;disabled:boolean;canWrite:boolean;selector:SourceAnchor;onSelectorChange:(selector:SourceAnchor)=>void;onRun:(request:RunRequest)=>Promise<void>;artifactURL:(artifact:FileArtifact,jobID?:string)=>string|undefined;downloadURL:(versionID?:string)=>string;api:FileApi};
export type FileEvidence={id?:string;text?:string;version_id:string;representation_id?:string;anchor?:SourceAnchor;score?:number;quote_hash?:string;[key:string]:unknown};

export const sourceID=(source:FileSource)=>source.file_id||source.id;
export const versionID=(version:FileVersion)=>version.version_id||version.id||'';
export function fileKind(name:string,mime=''):FileKind{
 const ext=name.toLowerCase().split('.').pop()||'';
 if(ext==='pdf')return 'pdf';if(ext==='docx')return 'docx';if(ext==='xlsx')return 'xlsx';if(ext==='pptx')return 'pptx';if(ext==='ipynb')return 'notebook';
 if(['png','jpg','jpeg','webp','gif','bmp','tiff','tif'].includes(ext)||mime.startsWith('image/'))return 'image';
 if(['zip','tar','tgz','gz'].includes(ext))return 'archive';
 if(['csv','tsv','parquet','jsonl'].includes(ext))return 'table';
 if(['txt','md','markdown','json','yaml','yml','xml','html','css','js','ts','tsx','jsx','py','go','rs','sql','log'].includes(ext)||mime.startsWith('text/')||mime.includes('json'))return 'text';
 if(mime==='application/pdf')return 'pdf';if(mime.includes('wordprocessingml'))return 'docx';if(mime.includes('spreadsheetml'))return 'xlsx';if(mime.includes('presentationml'))return 'pptx';return 'unknown';
}
export function unpackResult(value:unknown):FileResult{if(typeof value==='string'){try{return JSON.parse(value);}catch{return {message:value};}}return value&&typeof value==='object'?value as FileResult:{};}
export function normalizeJob(raw:any):FileJob{return {...raw,id:raw.id||raw.job_id,result:unpackResult(raw.result)};}
export const terminalJob=(status:string)=>['succeeded','completed','done','failed','cancelled','canceled','error','partial','unsupported','expired'].includes(status);
export const succeededJob=(status:string)=>['succeeded','completed','done','partial'].includes(status);
export function byteLabel(bytes?:number){if(bytes==null)return '';return bytes<1024?bytes+' B':bytes<1048576?(bytes/1024).toFixed(0)+' KB':(bytes/1048576).toFixed(1)+' MB';}
export function timeLabel(value:unknown,zh:boolean){if(value==null)return '';const date=new Date(value as any);return Number.isNaN(date.getTime())?String(value):date.toLocaleString(zh?'zh-CN':'en',{dateStyle:'medium',timeStyle:'short'});}
export function operationLabel(op:string,zh:boolean){const values:Record<string,[string,string]>={inspect:['文件检查','Inspect'],read:['读取选区','Read selection'],render:['生成预览','Render preview'],archive_list:['列出归档','List archive'],extract:['提取文件','Extract files'],query:['查询数据','Query data'],edit:['准备修改','Prepare changes'],recalculate:['重算工作簿','Recalculate workbook'],notebook_execute:['执行笔记本','Run notebook'],index:['解析资料','Parse source']};return values[op]?.[zh?0:1]||op;}
export function statusLabel(status:string,zh:boolean){const values:Record<string,[string,string]>={pending:['等待处理','Queued'],running:['处理中','Processing'],completed:['已完成','Completed'],succeeded:['已完成','Completed'],partial:['部分完成 · 请核对','Partial · review needed'],failed:['处理失败','Failed'],cancelled:['已取消','Cancelled'],unsupported:['暂不支持','Unsupported'],expired:['结果已过期','Expired'],poll_error:['暂时无法同步','Sync interrupted']};return values[status]?.[zh?0:1]||status;}
export function anchorLabel(anchor:SourceAnchor={},zh=false){const parts:string[]=[];if(typeof anchor.page_index==='number')parts.push((zh?'第 ':'Page ')+(anchor.page_index+1)+(zh?' 页':''));if(typeof anchor.slide_index==='number')parts.push((zh?'幻灯片 ':'Slide ')+(anchor.slide_index+1));if(anchor.sheet)parts.push(String(anchor.sheet));if(anchor.range||anchor.cell)parts.push(String(anchor.range||anchor.cell));if(typeof anchor.block_index==='number')parts.push((zh?'段落 ':'Paragraph ')+(anchor.block_index+1));if(typeof anchor.paragraph_index==='number')parts.push((zh?'段落 ':'Paragraph ')+(anchor.paragraph_index+1));if(typeof anchor.cell_index==='number')parts.push((zh?'代码单元 ':'Cell ')+(anchor.cell_index+1));if(anchor.start_line||anchor.line)parts.push((zh?'行 ':'Line ')+String(anchor.start_line||anchor.line));if(anchor.member)parts.push(String(anchor.member));if(anchor.shape_id!=null)parts.push((zh?'形状 ':'Shape ')+String(anchor.shape_id));return parts.join(' · ')||(zh?'文件位置':'Source location');}

export function selectionLabel(selection:SourceAnchor={},zh=false){
 const range=(values:unknown,label:string)=>Array.isArray(values)?label+values.map(v=>typeof v==='number'?v+1:String(v)).join(', '):'';
 if(Array.isArray(selection.pages))return range(selection.pages,zh?'页码 ':'Pages ');
 if(Array.isArray(selection.slides))return range(selection.slides,zh?'幻灯片 ':'Slides ');
 if(Array.isArray(selection.cells)||Array.isArray(selection.cell_indices))return range(selection.cells||selection.cell_indices,zh?'代码单元 ':'Cells ');
 if(selection.members&&Array.isArray(selection.members))return (zh?'归档成员 ':'Archive members ')+selection.members.join(', ');
 if(selection.start_line!=null)return (zh?'行 ':'Lines ')+selection.start_line+(selection.max_lines!=null?'–'+(Number(selection.start_line)+Number(selection.max_lines)-1):'');
 if(selection.start!=null||selection.count!=null)return (zh?'条目 ':'Items ')+(Number(selection.start||0)+1)+(selection.count!=null?'–'+(Number(selection.start||0)+Number(selection.count)):'');
 if(selection.row_offset!=null||selection.row_limit!=null)return (zh?'数据行 ':'Rows ')+(Number(selection.row_offset||0)+1)+(selection.row_limit!=null?'–'+(Number(selection.row_offset||0)+Number(selection.row_limit)):'');
 if(!Object.keys(selection).length)return zh?'默认读取范围':'Default read selection';
 return anchorLabel(selection,zh);
}

import React from 'react';
import {fileIconKind} from './fileIconKind.js';

// A compact, scalable illustration set shared by the tree, list, grid and
// details pane. No fonts, external icons, bitmap assets or filesystem probes.
function FileMark({kind}){
 switch(kind){
  case 'pdf':return <><rect x="10.2" y="17.8" width="20" height="11.6" rx="2.4" fill="var(--fg-color)"/><text x="20.2" y="25.9" fill="#fff" textAnchor="middle" fontSize="8.5" fontWeight="800" letterSpacing=".3">PDF</text></>;
  case 'document':return <g fill="none" stroke="var(--fg-color)" strokeWidth="2.1" strokeLinecap="round"><path d="M13.5 17.4h13M13.5 21.8h13M13.5 26.2h9.5"/></g>;
  case 'spreadsheet':return <><rect x="12.4" y="16.7" width="15.7" height="13.3" rx="1.6" fill="var(--fg-color)" opacity=".16"/><rect x="12.4" y="16.7" width="15.7" height="13.3" rx="1.6" fill="none" stroke="var(--fg-color)" strokeWidth="1.8"/><path d="M12.4 21.2h15.7M12.4 25.5h15.7M19 16.7v13.3" stroke="var(--fg-color)" strokeWidth="1.5"/></>;
  case 'presentation':return <><rect x="12" y="17" width="16.5" height="10.2" rx="1.6" fill="var(--fg-color)" opacity=".15"/><rect x="12" y="17" width="16.5" height="10.2" rx="1.6" fill="none" stroke="var(--fg-color)" strokeWidth="1.8"/><path d="M20.2 27.2v3m-4.2 0h8.4m-8.4-8.8 3 2.2 4.5-4.3" stroke="var(--fg-color)" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>;
  case 'image':return <><rect x="11.8" y="16.5" width="17" height="13.8" rx="2.3" fill="var(--fg-color)" opacity=".13"/><rect x="11.8" y="16.5" width="17" height="13.8" rx="2.3" fill="none" stroke="var(--fg-color)" strokeWidth="1.8"/><circle cx="17.2" cy="21.1" r="1.9" fill="var(--fg-color)"/><path d="m13.4 28 5-4.6 3 2.9 3.2-3.8 2.6 4" fill="none" stroke="var(--fg-color)" strokeWidth="1.6" strokeLinejoin="round" strokeLinecap="round"/></>;
  case 'video':return <><rect x="11.7" y="17" width="17.2" height="12.4" rx="2.7" fill="var(--fg-color)" opacity=".13"/><rect x="11.7" y="17" width="17.2" height="12.4" rx="2.7" fill="none" stroke="var(--fg-color)" strokeWidth="1.8"/><path d="m18.3 20.3 6.3 3-6.3 3z" fill="var(--fg-color)"/></>;
  case 'audio':return <><path d="M19.5 18.8v10M19.5 19l8-1.6v9.4" fill="none" stroke="var(--fg-color)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/><ellipse cx="16.2" cy="29" rx="3.3" ry="2.1" fill="var(--fg-color)"/><ellipse cx="24.2" cy="26.7" rx="3.3" ry="2.1" fill="var(--fg-color)"/></>;
  case 'archive':return <><path d="M19.9 15.8v9.6" stroke="var(--fg-color)" strokeWidth="3.3" strokeDasharray="2.1 2.1"/><path d="M16.4 27h7.1v4.3h-7.1z" rx="1" fill="var(--fg-color)" opacity=".16"/><rect x="16.4" y="26.6" width="7.1" height="4.3" rx="1" fill="none" stroke="var(--fg-color)" strokeWidth="1.5"/></>;
  case 'code':return <g fill="none" stroke="var(--fg-color)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="m16.5 19.4-4 4 4 4m7.8-8-4 4 4 4M22 17.6 18.7 29"/></g>;
  case 'config':return <g fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinecap="round"><path d="M13.5 18.7h13M13.5 24h13M13.5 29.2h13"/><circle cx="18.4" cy="18.7" r="2" fill="var(--fg-surface)" stroke="var(--fg-color)"/><circle cx="23" cy="24" r="2" fill="var(--fg-surface)" stroke="var(--fg-color)"/><circle cx="17.2" cy="29.2" r="2" fill="var(--fg-surface)" stroke="var(--fg-color)"/></g>;
  case 'text':return <g fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinecap="round"><path d="M13.2 18.2h13.5M13.2 22.6h13.5M13.2 27h11.1M13.2 31h7"/></g>;
  case 'font':return <text x="20.3" y="27.4" textAnchor="middle" fill="var(--fg-color)" fontSize="15" fontWeight="800" fontFamily="system-ui, sans-serif">Aa</text>;
  case 'application':return <g fill="var(--fg-color)"><rect x="12.3" y="17" width="6.8" height="6.8" rx="1.4"/><rect x="21.4" y="17" width="6.8" height="6.8" rx="1.4" opacity=".65"/><rect x="12.3" y="26.1" width="6.8" height="6.8" rx="1.4" opacity=".65"/><rect x="21.4" y="26.1" width="6.8" height="6.8" rx="1.4" opacity=".85"/></g>;
  default:return <g fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinecap="round"><path d="M13.4 19h13M13.4 23.7h13M13.4 28.4h8"/></g>;
 }
}

export default function FileGlyph({name,isDir=false,variant='list'}){
 const kind=fileIconKind(name,isDir);
 return <span className={'v4-file-glyph v4-glyph-'+kind+' v4-glyph-'+variant} aria-hidden="true">
  {kind==='folder'?
   <svg viewBox="0 0 40 40" fill="none" focusable="false">
    <path d="M4.2 12.8V10a3.3 3.3 0 0 1 3.3-3.3h8.3l3.5 3.6h12.2a3.7 3.7 0 0 1 3.7 3.7v17.3a3.5 3.5 0 0 1-3.5 3.5H7.7a3.5 3.5 0 0 1-3.5-3.5V12.8Z" fill="var(--fg-folder-back)"/>
    <path d="M4.1 18.4a3.6 3.6 0 0 1 3.6-3.6h24.6a3.6 3.6 0 0 1 3.6 3.6v12.8a3.6 3.6 0 0 1-3.6 3.6H7.7a3.6 3.6 0 0 1-3.6-3.6V18.4Z" fill="var(--fg-folder-front)"/>
    <path d="M7.8 16.7h24.4" stroke="#fff" strokeWidth="1.3" opacity=".55" strokeLinecap="round"/>
    <path d="M5.8 32.3c.6.6 1.3.9 2.2.9h23.6" stroke="var(--fg-folder-edge)" strokeWidth="1.1" opacity=".5" strokeLinecap="round"/>
   </svg>:
   <svg viewBox="0 0 40 40" fill="none" focusable="false">
    <path d="M11.4 3.7h12.1L31 11.2v21.3a3.1 3.1 0 0 1-3.1 3.1H11.4a3.1 3.1 0 0 1-3.1-3.1V6.8a3.1 3.1 0 0 1 3.1-3.1Z" fill="var(--fg-surface)" stroke="var(--fg-paper-border)" strokeWidth="1.35"/>
    <path d="M23.5 3.9v5.8a2.2 2.2 0 0 0 2.2 2.2h5.2" fill="var(--fg-fold)" stroke="var(--fg-paper-border)" strokeWidth="1.35" strokeLinejoin="round"/>
    <FileMark kind={kind}/>
   </svg>}
 </span>;
}

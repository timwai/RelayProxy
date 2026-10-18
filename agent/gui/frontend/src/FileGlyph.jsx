import React from 'react';
import {fileIconKind} from './fileIconKind.js';

// A compact, scalable illustration set shared by the tree, list, grid and
// details pane. No external icons, bitmap assets or filesystem probes.
function LetterMark({label}){
 return <><rect x="10.7" y="17.1" width="19.1" height="12.6" rx="3" fill="var(--fg-color)" opacity=".13"/>
  <text x="20.25" y="25.8" textAnchor="middle" fill="var(--fg-color)" fontSize={label.length>2?8.1:10.2} fontWeight="800" fontFamily="system-ui,sans-serif">{label}</text></>;
}
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

  case 'markdown':return <LetterMark label="MD"/>;
  case 'json':return <><path d="M16.4 18q-3 0-3 3v1q0 2-2 2 2 0 2 2v1q0 3 3 3m7.7-12q3 0 3 3v1q0 2 2 2-2 0-2 2v1q0 3-3 3" fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinecap="round"/><circle cx="20.4" cy="24" r="2.3" fill="var(--fg-color)"/></>;
  case 'javascript':return <LetterMark label="JS"/>;
  case 'typescript':return <LetterMark label="TS"/>;
  case 'python':return <LetterMark label="Py"/>;
  case 'golang':return <LetterMark label="Go"/>;
  case 'rust':return <LetterMark label="Rs"/>;
  case 'notebook':return <><rect x="12" y="17" width="15.8" height="12.6" rx="2" fill="var(--fg-color)" opacity=".12"/><path d="M16 17v12.6M19.4 20.7l2.4 2.2-2.4 2.2M22.7 26.6h2.5" stroke="var(--fg-color)" strokeWidth="1.9" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>;
  case 'web':return <><circle cx="20" cy="24" r="8.1" fill="none" stroke="var(--fg-color)" strokeWidth="1.7"/><ellipse cx="20" cy="24" rx="3.3" ry="8.1" fill="none" stroke="var(--fg-color)" strokeWidth="1.5"/><path d="M12.2 24h15.6M14 20h12M14 28h12" stroke="var(--fg-color)" strokeWidth="1.4"/></>;
  case 'stylesheet':return <LetterMark label="CSS"/>;
  case 'database':return <><ellipse cx="20" cy="18.7" rx="8" ry="3.2" fill="var(--fg-color)" opacity=".24"/><path d="M12 18.7v10.1c0 4 16 4 16 0V18.7M12 23.6c0 4 16 4 16 0" fill="none" stroke="var(--fg-color)" strokeWidth="1.9"/><ellipse cx="20" cy="18.7" rx="8" ry="3.2" fill="none" stroke="var(--fg-color)" strokeWidth="1.8"/></>;
  case 'data':return <><rect x="12" y="25" width="3.5" height="6.1" rx="1" fill="var(--fg-color)" opacity=".6"/><rect x="18.2" y="19" width="3.5" height="12.1" rx="1" fill="var(--fg-color)"/><rect x="24.3" y="22" width="3.5" height="9.1" rx="1" fill="var(--fg-color)" opacity=".75"/></>;
  case 'vector':return <><path d="M13 29 19.7 18l7.9 10.1M13 29q7-11.3 14.6-.9" fill="none" stroke="var(--fg-color)" strokeWidth="1.7"/>{[[13,29],[19.7,18],[27.6,28.1]].map(([x,y],i)=><rect key={i} x={x-1.9} y={y-1.9} width="3.8" height="3.8" rx=".5" fill="var(--fg-surface)" stroke="var(--fg-color)" strokeWidth="1.3"/>)}</>;
  case 'design':return <><path d="m12.3 28.6 1.5-4.8 11.8-11 4.7 4.9-11.7 11.1-4.8 1.3z" transform="translate(-1 7) scale(.92)" fill="var(--fg-color)" opacity=".15"/><path d="m12.6 30.3 3.5-1 12.3-12.2-3.6-3.6-12.2 12.4z" transform="translate(0 2)" fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinejoin="round"/></>;
  case 'model3d':return <g fill="none" stroke="var(--fg-color)" strokeWidth="1.7" strokeLinejoin="round"><path d="m20 15 8 4.6v9L20 33l-8-4.4v-9zM12 19.6l8 4.7 8-4.7M20 24.3V33"/></g>;
  case 'subtitle':return <><path d="M12 18h16v10H20l-4 3v-3h-4z" stroke="var(--fg-color)" fill="var(--fg-color)" fillOpacity=".1" strokeWidth="1.8" strokeLinejoin="round"/><path d="M15 22h10M15 25h7" stroke="var(--fg-color)" strokeWidth="1.6" strokeLinecap="round"/></>;
  case 'disk':return <><circle cx="20" cy="23.8" r="8.8" fill="var(--fg-color)" opacity=".13"/><circle cx="20" cy="23.8" r="8.8" stroke="var(--fg-color)" strokeWidth="1.8"/><circle cx="20" cy="23.8" r="2.5" fill="var(--fg-surface)" stroke="var(--fg-color)" strokeWidth="1.5"/><path d="m15.7 15.9 2.5 4" stroke="var(--fg-color)" strokeWidth="1.7"/></>;
  case 'ebook':return <><path d="M20 19c-3.2-2.4-5.5-2.2-8.8-1.8v11.8c3.3-.5 6-.2 8.8 2.2 2.8-2.4 5.5-2.7 8.8-2.2V17.2c-3.3-.4-5.6-.6-8.8 1.8zM20 19v12.2" stroke="var(--fg-color)" strokeWidth="1.8" fill="var(--fg-color)" fillOpacity=".1" strokeLinejoin="round"/></>;
  case 'certificate':return <><path d="M20 16.5 27 19v6.4c0 3.8-3.3 6.5-7 8.3-3.7-1.8-7-4.5-7-8.3V19z" fill="var(--fg-color)" opacity=".14"/><path d="M20 16.5 27 19v6.4c0 3.8-3.3 6.5-7 8.3-3.7-1.8-7-4.5-7-8.3V19zM16.7 25l2.4 2.2 4.2-4.9" fill="none" stroke="var(--fg-color)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"/></>;
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

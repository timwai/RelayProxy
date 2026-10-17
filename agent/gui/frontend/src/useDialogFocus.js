import {useEffect,useRef} from 'react';

// Reuse the same modal focus management across the Agent shell and file explorer.
export default function useDialogFocus(open, dialogRef, onClose){
 const onCloseRef=useRef(onClose);onCloseRef.current=onClose;
 useEffect(()=>{
  if(!open||!dialogRef.current)return;
  const dialog=dialogRef.current,previous=document.activeElement;
  const selector='a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"])';
  const focusable=()=>Array.from(dialog.querySelectorAll(selector)).filter(el=>el.getClientRects().length>0&&!el.closest('[aria-hidden="true"]'));
  (focusable().find(el=>/^(INPUT|SELECT|TEXTAREA)$/.test(el.tagName))||focusable()[0]||dialog).focus();
  const onKey=e=>{
   if(e.key==='Escape'){e.preventDefault();onCloseRef.current?.();return}
   if(e.key!=='Tab')return;
   const items=focusable();
   if(!items.length){e.preventDefault();dialog.focus();return}
   const first=items[0],last=items[items.length-1],active=document.activeElement;
   if(e.shiftKey&&(active===first||!dialog.contains(active))){e.preventDefault();last.focus()}
   else if(!e.shiftKey&&(active===last||!dialog.contains(active))){e.preventDefault();first.focus()}
  };
  document.addEventListener('keydown',onKey);
  return()=>{document.removeEventListener('keydown',onKey);if(previous instanceof HTMLElement&&previous.isConnected)previous.focus()};
 },[open,dialogRef]);
}

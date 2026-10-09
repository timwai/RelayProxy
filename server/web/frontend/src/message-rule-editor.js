export function parseRuleKeywords(text){
  return [...new Set(String(text??'').split(/[,，\n]/).map(s=>s.trim()).filter(Boolean))];
}
export function createMessageRule(){
  return {name:'新消息规则',type:'message',enabled:true,popup:true,
    match:{matchType:'keywords',keywordMode:'any',keywords:[]}};
}
export function changeRuleType(rule,type){
  const next={...rule,type};
  if(type==='verification_code'){
    next.verification=rule.verification||{type:'auto',minLength:4,maxLength:8,maxDistance:64,
      allowLetters:true,allowDigits:true,requireDigit:true};
  }else{delete next.verification;}
  return next;
}
export function validateDraftMessageRules(rules){
  for(const [i,rule] of rules.entries()){
    const label=rule.name?.trim()||'规则 '+(i+1);
    if(!rule.name?.trim())return '第 '+(i+1)+' 条消息规则需要名称';
    const match=rule.match||{},type=match.matchType||'keywords';
    if(type==='keywords'&&!Array.isArray(match.keywords)||type==='keywords'&&!match.keywords.some(x=>String(x).trim()))
      return '「'+label+'」至少需要一个关键词；请先完成新增规则再保存';
    if((type==='contains'||type==='regex')&&!String(match.pattern||'').trim())
      return '「'+label+'」需要填写匹配内容';
    if(rule.type==='verification_code'){
      if(!rule.verification)return '「'+label+'」需要验证码提取规则';
      if(rule.verification.type==='regex'&&!String(rule.verification.pattern||'').trim())
        return '「'+label+'」需要填写验证码提取正则';
    }
  }
  return '';
}

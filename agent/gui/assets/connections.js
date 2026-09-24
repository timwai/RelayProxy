(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const states = {connecting:'连接中',active:'活跃',closed:'已结束',failed:'失败',rejected:'已阻断'};
  const entries = {transparent:'透明代理',socks5:'SOCKS5',http:'HTTP'};
  const actions = {PROXY:'代理',DIRECT:'直连',REJECT:'阻断'};
  const ruleNames = {default:'默认动作',global_proxy:'全局代理',direct:'全局直连',exclude:'排除进程','loop-guard':'回环保护'};
  let snapshot = {connections:[]}, paused = false, page = 0, selected = null, sort = 'started_at', descending = true;
  const pageSize = 200;
  const text = value => String(value == null ? '' : value);
  const isActive = row => row.state === 'active' || row.state === 'connecting';
  function bytes(value, rate = false) {
    let n = Math.max(0,Number(value)||0), unit = 0;
    const units = ['B','KB','MB','GB','TB'];
    while (n >= 1024 && unit < units.length - 1) { n /= 1024; unit++; }
    return n.toLocaleString('zh-CN',{maximumFractionDigits:unit ? 1 : 0}) + ' ' + units[unit] + (rate ? '/s' : '');
  }
  function duration(seconds) {
    const n = Math.max(0,Math.floor(Number(seconds)||0));
    return n < 60 ? n+' 秒' : n < 3600 ? Math.floor(n/60)+' 分 '+n%60+' 秒' : Math.floor(n/3600)+' 时 '+Math.floor(n%3600/60)+' 分';
  }
  function startedAt(value) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN',{hour12:false});
  }
  function sortValue(row, key) {
    if (key === 'host') return row.host || row.ip || '';
    if (key === 'started_at') {
      const value = Date.parse(row.started_at);
      return Number.isNaN(value) ? 0 : value;
    }
    return row[key];
  }
  function cell(row, primary, secondary, className = '', title = '') {
    const td = document.createElement('td'); td.className = className;
    const main = document.createElement('span'); main.textContent = primary;
    if (className === 'destination') main.className = 'target';
    if (className === 'identity') main.className = 'process';
    td.appendChild(main);
    if (secondary) { const sub = document.createElement('span'); sub.className = 'sub'; sub.textContent = secondary; td.appendChild(sub); }
    if (title) td.title = title;
    row.appendChild(td); return td;
  }
  function renderDetails(rows) {
    const record = rows.find(row => row.id === selected);
    $('details').hidden = !record;
    if (!record) return;
    const values = [
      ['进程',record.process || '未识别'],['本地端点',record.source || '未知'],['目标',record.host || record.ip || '未知'],
      ['目标 IP',record.ip || '由出口解析，未返回 IP'],['域名来源',record.domain_source === 'requested' ? '应用请求' : record.domain_source === 'dns' ? 'DNS 应答关联' : '未知'],['出口',record.exit_id || (record.action === 'PROXY' ? '自动选择' : '本地')],
      ['命中规则',ruleNames[record.rule] || record.rule || '未命名规则'],['开始时间',new Date(record.started_at).toLocaleString()],['计数方式',record.accounting === 'packet' ? '数据包载荷（含重传）' : '传输载荷']
    ];
    $('details-grid').replaceChildren();
    values.forEach(([label,value]) => { const item = document.createElement('div'); const key = document.createElement('strong'); key.textContent = label+'：'; item.append(key,document.createTextNode(text(value))); $('details-grid').appendChild(item); });
    $('detail-error').textContent = record.error || '';
  }
  function render() {
    const query = $('search').value.trim().toLowerCase(), state = $('state-filter').value;
    const protocol = $('protocol-filter').value, action = $('action-filter').value;
    const rows = snapshot.connections.filter(row => {
      if (state !== 'all' && row.state !== state) return false;
      if (protocol && row.protocol !== protocol || action && row.action !== action) return false;
      return !query || [row.process,row.process_name,row.pid,row.host,row.ip,row.port,row.rule,row.source].some(value => text(value).toLowerCase().includes(query));
    });
    rows.sort((a,b) => {
      const x = sortValue(a,sort), y = sortValue(b,sort);
      const difference = typeof x === 'number' && typeof y === 'number' ? x-y : text(x).localeCompare(text(y));
      return difference ? (descending ? -difference : difference) : b.id-a.id;
    });
    const pages = Math.max(1,Math.ceil(rows.length/pageSize)); page = Math.min(page,pages-1);
    const body = $('connections'), fragment = document.createDocumentFragment();
    rows.slice(page*pageSize,(page+1)*pageSize).forEach(record => {
      const row = document.createElement('tr'); row.dataset.id = record.id; row.tabIndex = 0;
      row.setAttribute('aria-selected',String(selected === record.id));
      cell(row,record.process_name || '未知进程',record.pid ? 'PID '+record.pid : '未识别 / 远程客户端','identity',record.process);
      cell(row,record.host || record.ip || '未知目标',record.host ? (record.ip || 'IP 由出口解析') + (record.domain_source === 'dns' ? ' · DNS 关联' : '') : '域名未知','destination',record.host || record.ip);
      cell(row,record.port || '—'); cell(row,text(record.protocol).toUpperCase(),entries[record.entry] || record.entry);
      cell(row,actions[record.action] || record.action,ruleNames[record.rule] || record.rule || '未命名规则');
      cell(row,bytes(record.upload_rate,true),'','num'); cell(row,bytes(record.download_rate,true),'','num');
      cell(row,bytes(record.upload),'','num'); cell(row,bytes(record.download),'','num');
      const status = cell(row,''); status.firstChild.className = 'state '+record.state; status.firstChild.textContent = states[record.state] || record.state;
      cell(row,startedAt(record.started_at));
      cell(row,duration(record.duration),'','num');
      const select = () => { selected = record.id; render(); };
      row.onclick = select; row.onkeydown = event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); select(); } };
      fragment.appendChild(row);
    });
    if (!rows.length) { const row = document.createElement('tr'), td = document.createElement('td'); td.colSpan = 12; td.className = 'empty'; td.textContent = query || protocol || action || state !== 'all' ? '没有符合筛选条件的连接' : '暂无连接记录'; row.appendChild(td); fragment.appendChild(row); }
    body.replaceChildren(fragment);
    $('row-count').textContent = rows.length+' 条符合条件 · 本次共 '+(snapshot.total || 0)+' 条连接'+(snapshot.omitted ? ' · '+snapshot.omitted+' 条未保留明细（达到容量上限）' : '');
    $('page').textContent = (page+1)+' / '+pages; $('previous').disabled = page === 0; $('next').disabled = page+1 >= pages;
    renderDetails(snapshot.connections);
  }
  function display(next) {
    snapshot = next; snapshot.connections = Array.isArray(next.connections) ? next.connections : [];
    $('active').textContent = next.active || 0;
    ['upload','download'].forEach(key => { $(key).textContent = bytes(next[key]); $(key+'-rate').textContent = bytes(next[key+'_rate'],true); });
    $('sample-time').textContent = '更新于 '+new Date(next.sampled_at).toLocaleTimeString();
    render();
  }
  async function poll() {
    try {
      if (!paused) {
        if (typeof window.goGetConnections !== 'function') throw new Error('监控尚未连接到 Agent');
        const raw = await window.goGetConnections();
        if (!raw) return;
        if (!paused) { display(typeof raw === 'string' ? JSON.parse(raw) : raw); $('error').hidden = true; $('live-label').textContent = '实时更新'; $('live-state').classList.remove('muted'); }
      }
    } catch (error) {
      $('error').hidden = false; $('error').textContent = '读取连接失败：'+error.message;
      $('live-label').textContent = '等待恢复'; $('live-state').classList.add('muted');
    } finally { setTimeout(poll,1000); }
  }
  ['search','state-filter','protocol-filter','action-filter'].forEach(id => $(id).addEventListener(id === 'search' ? 'input' : 'change',() => { page = 0; render(); }));
  $('pause').onclick = () => { paused = !paused; $('pause').textContent = paused ? '继续刷新' : '暂停刷新'; $('pause').setAttribute('aria-pressed',String(paused)); $('live-label').textContent = paused ? '已暂停刷新' : '正在更新'; $('live-state').classList.toggle('muted',paused); };
  $('clear').onclick = async () => {
    const button = $('clear');
    button.disabled = true;
    try {
      if (typeof window.goClearConnections !== 'function') throw new Error('当前界面不支持清空连接历史');
      await window.goClearConnections();
      selected = null; page = 0;
      if (typeof window.goGetConnections === 'function') {
        const raw = await window.goGetConnections();
        if (raw) display(typeof raw === 'string' ? JSON.parse(raw) : raw);
      } else {
        snapshot.connections = snapshot.connections.filter(isActive);
        render();
      }
      $('error').hidden = true;
    } catch (error) {
      $('error').hidden = false; $('error').textContent = '清空连接历史失败：'+error.message;
    } finally {
      button.disabled = false;
    }
  };
  $('previous').onclick = () => { page--; render(); }; $('next').onclick = () => { page++; render(); };
  document.querySelectorAll('[data-sort]').forEach(button => button.onclick = () => {
    if (sort === button.dataset.sort) descending = !descending; else { sort = button.dataset.sort; descending = true; }
    document.querySelectorAll('th').forEach(th => th.removeAttribute('aria-sort')); button.parentElement.setAttribute('aria-sort',descending ? 'descending' : 'ascending'); render();
  });
  poll();
})();

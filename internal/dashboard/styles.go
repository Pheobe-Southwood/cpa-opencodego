package dashboard

// pageStyles is the page's single stylesheet. It uses custom properties so the
// dark theme is one override block, and it keeps the layout to one column with a
// responsive key grid because the page lives inside the management centre's
// iframe on desktop and on a phone.
const pageStyles = `
:root{
  --bg:#f6f7f9;
  --panel:#ffffff;
  --panel-2:#fbfbfd;
  --text:#1b1f27;
  --text-soft:#5c6470;
  --text-faint:#8b93a1;
  --line:#e3e6eb;
  --accent:#4f7cff;
  --accent-soft:rgba(79,124,255,.12);
  --ok:#1f9d61;
  --ok-soft:rgba(31,157,97,.12);
  --warn:#d08413;
  --warn-soft:rgba(208,132,19,.14);
  --danger:#d14343;
  --danger-soft:rgba(209,67,67,.12);
  --track:#eceff4;
  --radius:12px;
  --shadow:0 1px 2px rgba(16,24,40,.06),0 8px 24px rgba(16,24,40,.04);
  --host-header:0px;
}
@media (prefers-color-scheme: dark){
  :root:not([data-theme="light"]){
    --bg:#0f1116;
    --panel:#171a21;
    --panel-2:#1b1f27;
    --text:#e8ebf1;
    --text-soft:#a7aebc;
    --text-faint:#767e8d;
    --line:#262b35;
    --accent:#7b9cff;
    --accent-soft:rgba(123,156,255,.16);
    --ok:#4ec98a;
    --ok-soft:rgba(78,201,138,.14);
    --warn:#e6b047;
    --warn-soft:rgba(230,176,71,.16);
    --danger:#f07373;
    --danger-soft:rgba(240,115,115,.14);
    --track:#242a34;
    --shadow:0 1px 2px rgba(0,0,0,.4),0 8px 24px rgba(0,0,0,.28);
  }
}
[data-theme="dark"]{
  --bg:#0f1116;
  --panel:#171a21;
  --panel-2:#1b1f27;
  --text:#e8ebf1;
  --text-soft:#a7aebc;
  --text-faint:#767e8d;
  --line:#262b35;
  --accent:#7b9cff;
  --accent-soft:rgba(123,156,255,.16);
  --ok:#4ec98a;
  --ok-soft:rgba(78,201,138,.14);
  --warn:#e6b047;
  --warn-soft:rgba(230,176,71,.16);
  --danger:#f07373;
  --danger-soft:rgba(240,115,115,.14);
  --track:#242a34;
  --shadow:0 1px 2px rgba(0,0,0,.4),0 8px 24px rgba(0,0,0,.28);
}
*{box-sizing:border-box}
html,body{margin:0;padding:0}
body{
  background:var(--bg);
  color:var(--text);
  font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;
  -webkit-font-smoothing:antialiased;
}
.page{
  max-width:1180px;
  margin:0 auto;
  padding:calc(var(--host-header) + 20px) 20px 40px;
}
.bar{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;flex-wrap:wrap}
.brand{display:flex;align-items:center;gap:12px;min-width:0}
.mark{width:38px;height:38px;flex:0 0 auto;border-radius:10px;overflow:hidden;display:block;background:#111318}
.mark svg{width:100%;height:100%;display:block}
h1{margin:0;font-size:18px;font-weight:650;letter-spacing:.01em}
.sub{margin:2px 0 0;color:var(--text-faint);font-size:12px;max-width:min(60ch,72vw);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.actions{display:flex;align-items:center;gap:10px}
.btn{
  appearance:none;border:1px solid var(--line);background:var(--panel);color:var(--text);
  border-radius:9px;padding:7px 14px;font-size:13px;cursor:pointer;transition:.15s ease;
}
.btn:hover{border-color:var(--accent);color:var(--accent)}
.btn.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.btn.primary:hover{filter:brightness(1.06);color:#fff}
.btn[disabled]{opacity:.55;cursor:progress}
.toggle{display:inline-flex;align-items:center;gap:6px;color:var(--text-soft);font-size:13px;user-select:none;cursor:pointer}
.toggle input{accent-color:var(--accent);width:15px;height:15px;margin:0}
.banner{
  margin-top:16px;border:1px solid var(--line);border-left:3px solid var(--warn);
  background:var(--warn-soft);color:var(--text);border-radius:10px;padding:12px 14px;font-size:13px;white-space:pre-wrap;
}
.banner.error{border-left-color:var(--danger);background:var(--danger-soft)}
.banner.warn{border-left-color:var(--warn);background:var(--warn-soft)}
.hidden{display:none !important}
.chips{display:flex;flex-wrap:wrap;gap:10px;margin-top:18px}
.chip{
  display:flex;align-items:baseline;gap:8px;background:var(--panel);border:1px solid var(--line);
  border-radius:10px;padding:9px 14px;box-shadow:var(--shadow);min-width:116px;
}
.chip .k{color:var(--text-faint);font-size:12px}
.chip .v{font-size:17px;font-weight:650;font-variant-numeric:tabular-nums}
.chip .v.ok{color:var(--ok)}
.chip .v.warn{color:var(--warn)}
.chip .v.danger{color:var(--danger)}
.panel{margin-top:20px;background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);overflow:hidden}
.panel-head{display:flex;align-items:baseline;justify-content:space-between;gap:12px;padding:14px 18px;border-bottom:1px solid var(--line);flex-wrap:wrap}
.panel-head h2{margin:0;font-size:14px;font-weight:650}
.meta{color:var(--text-faint);font-size:12px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:14px;padding:16px}
.card{border:1px solid var(--line);border-radius:11px;background:var(--panel-2);padding:14px;display:flex;flex-direction:column;gap:12px;min-width:0}
.card-head{display:flex;align-items:center;justify-content:space-between;gap:10px;min-width:0}
.card-head code{font:12.5px/1.4 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.badge{flex:0 0 auto;font-size:11px;line-height:1;padding:5px 8px;border-radius:999px;background:var(--accent-soft);color:var(--accent);white-space:nowrap}
.badge.direct{background:var(--ok-soft);color:var(--ok)}
.groups-line{color:var(--text-faint);font-size:12px;display:flex;flex-wrap:wrap;gap:6px;min-width:0}
.groups-line span{background:var(--track);border-radius:6px;padding:2px 7px;color:var(--text-soft)}
.windows{display:flex;flex-direction:column;gap:11px}
.window{display:flex;flex-direction:column;gap:5px}
.window-head{display:flex;justify-content:space-between;gap:8px;font-size:12px;color:var(--text-soft)}
.window-head .pct{font-variant-numeric:tabular-nums;color:var(--text)}
.track{height:7px;border-radius:999px;background:var(--track);overflow:hidden}
.fill{height:100%;border-radius:999px;background:var(--ok);transition:width .35s ease}
.fill.warn{background:var(--warn)}
.fill.danger{background:var(--danger)}
.window-foot{display:flex;justify-content:space-between;gap:8px;font-size:11.5px;color:var(--text-faint)}
.window-foot .remain{font-variant-numeric:tabular-nums}
.card-error{font-size:12.5px;color:var(--danger);background:var(--danger-soft);border-radius:8px;padding:8px 10px;word-break:break-word}
.empty{padding:26px 18px;text-align:center;color:var(--text-faint);font-size:13px;grid-column:1/-1}
.groups{overflow-x:auto}
table{width:100%;min-width:860px;border-collapse:collapse;font-size:12.5px}
th,td{padding:10px 14px;text-align:left;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--text-faint);font-weight:500;white-space:nowrap}
tr:last-child td{border-bottom:none}
td.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;color:var(--text-soft);white-space:nowrap}
td.nowrap{white-space:nowrap}
.models{display:flex;flex-wrap:wrap;gap:5px;max-width:520px}
.models span{background:var(--track);border-radius:6px;padding:2px 7px;color:var(--text-soft);font-size:11.5px}
.tag-muted{color:var(--text-faint);font-size:11.5px}
.foot{margin-top:18px;color:var(--text-faint);font-size:12px;text-align:center}
.foot a{color:var(--accent);text-decoration:none}
.foot a:hover{text-decoration:underline}
@media (max-width:560px){
  .page{padding:calc(var(--host-header) + 14px) 12px 28px}
  .grid{grid-template-columns:1fr;padding:12px;gap:12px}
  .bar{gap:12px}
  .actions{width:100%;justify-content:flex-end}
}
`

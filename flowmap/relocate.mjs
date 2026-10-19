// Re-point the flowmap steps that are not functions (so qf-flowmap cannot
// re-find them) at the line that currently holds a known snippet.
// Usage: node relocate.mjs <repo dir>
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const repo = process.argv[2];
const path = join(repo, 'flowmap/flows.json');
const j = JSON.parse(readFileSync(path, 'utf8'));
const files = { ui: 'ui/index.html' };
const lineOf = (file, needle) => {
  const lines = readFileSync(join(repo, files[file] || file), 'utf8').split('\n');
  const i = lines.findIndex(l => l.includes(needle));
  if (i < 0) throw new Error(`"${needle}" not in ${file}`);
  return `${file}:${i + 1}`;
};

// [flow, step fn (or trigger btn with kind 't'), file, snippet]
const map = [
  ['Khởi động ứng dụng', 'srv.Serve()', 'main.go', 'srv.Serve(ln)'],
  ['Bấm shortcut lần hai', 'return', 'main.go', 'alreadyServing(url) {'],
  ['Đóng cửa sổ → thoát', 'cmd.Wait()', 'main.go', '_ = cmd.Wait()'],
  ['Đóng cửa sổ → thoát', '< 3 giây?', 'main.go', 'time.Since(started) < 3*time.Second'],
  ['Đóng cửa sổ → thoát', 'os.Exit(0)', 'main.go', 'os.Exit(0)'],
  ['Chọn thư mục mã nguồn', 'POST /api/pick-folder', 'ui', "const picked = await api('/api/pick-folder'"],
  ['Chọn thư mục mã nguồn', 'mountPath › dòng báo dưới ô', 'ui', 'const render = info =>'],
  ['Quét image có sẵn trên máy', 'imagesDialog › vẽ danh sách', 'ui', "$('#repos', m).innerHTML = `<div class=\"builds\">`"],
  ['Build một version', 'POST /api/build', 'ui', "const b = await api('/api/build', {method:'POST'"],
  ['Build một version', 'docker push', 'builder.go', 's.run(ctx, lb, env.Context, docker, "push", ref)'],
  ['Nhật ký trực tiếp', 'nối vào <pre>', 'ui', 'pre.textContent = text;'],
  ['Nhật ký trực tiếp', 'status !== running?', 'ui', "if (data.build.status === 'running'){"],
  ['Push bản build chưa push', 'nút Push', 'ui', "$$('[data-push]', host).forEach"],
  ['Promote / Rollback', 'cảnh báo trùng version', 'ui', 'if (existing && !existing.digest){'],
  ['Promote / Rollback', 'status === success?', 'api.go', 'if src.Status != "success" {'],
  ['Promote / Rollback', 'pushed && digest?', 'api.go', 'if !src.Pushed || src.Digest == "" {'],
  ['Promote / Rollback', 'cùng dự án, khác env', 'api.go', 'if project.ID != src.ProjectID {'],
  ['Promote / Rollback', 'tag đích ≠ tag nguồn', 'api.go', 'dst == src.Ref {'],
  ['Dừng build giữa chừng', 'nút Dừng build', 'ui', "$$('[data-cancel]', host).forEach"],
  ['Dừng build giữa chừng', 'context cancel', 'builder.go', 'cmd.Cancel = func() error {'],
  ['Xóa một bản build khỏi lịch sử', 'nút Xóa khỏi lịch sử', 'ui', "$$('[data-del]', host).forEach"],
  ['Xem trước lệnh build', 'POST /api/build/preview', 'ui', "plan = await api('/api/build/preview'"],
  ['Khai báo tham số lệnh build', 'PUT /api/config', 'ui', "const saved = await api('/api/config', {method:'PUT'"],
  ['Đọc tag từ registry', 'POST /api/registry/import', 'ui', "await api('/api/registry/import'"],
  ['Dọn image cũ', 'POST /api/cleanup', 'ui', "await api('/api/cleanup', {method:'POST'"],
  ['Thay đổi so với bản trước', 'GET /api/build/{id}/changes', 'ui', 'await api(`/api/build/${id}/changes`)'],
  ['t', 'Khởi động ứng dụng', 'Mở DIMA từ Start Menu hoặc dòng lệnh', 'main.go', 'func main() {'],
  ['t', 'Bấm shortcut lần hai', 'Mở DIMA lần thứ hai khi bản đầu còn chạy', 'main.go', 'alreadyServing(url) {'],
  ['t', 'Đóng cửa sổ → thoát', 'Đóng cửa sổ ứng dụng', 'main.go', 'func watchWindow('],
  ['t', 'Nạp trạng thái & vẽ giao diện', 'Sau mọi thao tác ghi dữ liệu', 'ui', 'await refresh();'],
  ['t', 'Nâng cấp dữ liệu 0.1.x → 0.2.0', 'Mở lần đầu sau khi nâng cấp', 'store.go', 'func (s *Store) loadConfig() error {'],
  ['t', 'Chặn request từ trang khác', 'Mọi request tới DIMA', 'main.go', 'func guard(next http.Handler)'],
];
let n = 0;
for (const row of map) {
  if (row[0] === 't') {
    const [, flowName, btn, file, needle] = row;
    const t = j.flows.find(f => f.name === flowName).triggers.find(t => t.btn === btn);
    t.file = lineOf(file, needle); n++;
    continue;
  }
  const [flowName, fn, file, needle] = row;
  const st = j.flows.find(f => f.name === flowName).steps.find(s => s.fn === fn);
  if (!st) throw new Error(`no step ${flowName} › ${fn}`);
  st.file = lineOf(file, needle); n++;
}
j.screens.find(s => s.key === 'app').file = lineOf('main.go', 'func main() {');
writeFileSync(path, JSON.stringify(j, null, 2) + '\n');
console.log('relocated', n + 1);

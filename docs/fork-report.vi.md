# Báo cáo fork Skillshare

Ngày kiểm tra: 20/09/2026. Nguồn upstream: commit `4415c7a7`. Mã bản vá: `a8c9b322`.

- [Fork và nhánh chứa bản vá](https://github.com/clover1417/skillshare/tree/codex/windows-sync-reliability)
- [PR #1 trong fork](https://github.com/clover1417/skillshare/pull/1)
- [CI Linux và Windows](https://github.com/clover1417/skillshare/actions/runs/35489588110)

## Những lỗi đã sửa

| Lỗi đã tái hiện | Cách sửa |
| --- | --- |
| Windows tạo junction thư mục cho `AGENTS.md` và custom agents, khiến file không đọc được. | Dùng symlink file khi có quyền; tự chuyển sang copy có theo dõi khi thiếu quyền. Sửa được junction file hỏng do bản cũ tạo ra. |
| File extras ở chế độ copy không cập nhật khi nguồn thay đổi. | Lưu nguồn và SHA-256 của lần đồng bộ trước. Cập nhật được cả khi trình soạn thảo thay file bằng rename. |
| Copy agent ghi đè file riêng; prune có thể xóa agent hoặc link ngoài phạm vi quản lý. | Chỉ cập nhật/xóa file đã nhận quản lý và còn nguyên nội dung. File sửa cục bộ được giữ lại. Khi gỡ target, kiểm tra cả nguồn sở hữu để tránh xóa file của extra khác. |
| JSON trả mã thành công dù có lỗi; `sync --all` nuốt lỗi extras. | CLI trả mã lỗi khác 0 với JSON hợp lệ. Text, TUI và HTTP cũng truyền lỗi ra ngoài. |
| Dry-run tạo/chuyển thư mục nguồn. Nguồn mất bị tạo lại thành thư mục rỗng. | Preview giữ nguyên các thư mục này; nguồn bị mất được báo lỗi để tránh prune nhầm. |
| MCP được áp dụng trước khi extras báo lỗi. | Kiểm tra xung đột MCP trước khi sync; chỉ áp dụng MCP sau khi các tài nguyên đồng bộ thành công. |
| Root `pull` bỏ qua extras/MCP; dashboard bỏ qua sửa target khi Git đã mới nhất. | Đồng bộ đủ skills, agents, extras và MCP, kể cả lúc Git không có commit mới. |
| Doctor/diff nhận bản copy do Skillshare quản lý là file riêng. | Nhận diện manifest và so sánh với nguồn. |
| Force sync thư mục có thể xóa nguồn khi nguồn và đích chồng lên nhau. | Chặn nguồn bị mất và cấu hình thư mục chồng lấn trước khi thay thế. |

Phần clean code tập trung vào đường đồng bộ: gộp xử lý extras global/project, dùng chung cơ chế ghi file và theo dõi nguồn cho agents, extras và file qua extension; khóa ghi theo thư mục và dùng file tạm khi thay nội dung.

## Kiểm chứng

- Linux: toàn bộ unit test, command test và integration test với race detector; kiểm tra format, `go vet` và build.
- Windows: regression test riêng, build executable và luồng CLI với 26 kiểm tra. Đã chạy trên máy hiện tại thiếu quyền symlink và runner Windows có quyền khác.
- Luồng thực tế gồm chia sẻ/tách riêng skills và MCP, giữ file riêng, cập nhật instructions, giữ sửa đổi cục bộ, force có chủ đích, root pull và lỗi JSON.
- Frontend build thành công. Dashboard thật đã hiển thị lỗi extras được cài sẵn, rồi báo sync thành công sau khi target được khôi phục. Không thấy lỗi console trình duyệt.
- Dữ liệu kiểm thử nằm trong môi trường giả lập riêng.

Bộ test gốc có các giả định Unix và một test TUI bị timeout trên Windows. Vì vậy kết quả Windows ở đây là bộ regression và luồng CLI riêng; bộ đầy đủ được kiểm chứng trên Linux.

## Bản chạy và phạm vi

Executable nằm tại `bin/skillshare.exe` trong checkout, phiên bản `0.21.1-fork.2`. Web assets tương ứng đã được build và lưu vào cache trên máy này. Nhánh chứa bản vá là `codex/windows-sync-reliability`; PR đang để draft trong fork.

Trên Windows thiếu quyền symlink, sau khi sửa instruction hoặc agent ở nguồn cần chạy sync để cập nhật bản copy. Manifest `.skillshare-files.json` và file lock bên cạnh target cần được giữ lại. File cũ chưa có thông tin sở hữu được bảo toàn; `--force` cho phép nhận quản lý sau khi bạn đã xem xung đột.

Công cụ vẫn đồng bộ từ nguồn ra các target và có lệnh collect/import riêng. Bản vá chưa thêm hòa giải hai chiều tự động. Sync nhiều tài nguyên cũng chưa có giao dịch rollback toàn bộ; lỗi ở target sau có thể xảy ra sau khi target trước đã cập nhật.

Git root tiếp tục loại `config.yaml` theo thiết kế upstream. Để mang sang máy khác, dùng `sources.mcp` trỏ tới YAML riêng và một config template đã bỏ bí mật. Biến môi trường, đăng nhập và đường dẫn theo máy cần thiết lập tại máy đích. Cơ chế self-upgrade của upstream có thể thay mất bản fork; nên build lại nhánh này hoặc chuyển các bản vá trước khi nâng cấp.

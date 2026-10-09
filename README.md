# DIMA — Docker Image Management

Ứng dụng desktop nhẹ để quản lý version image Docker theo **dự án** và theo **môi trường**: build nhanh, push nhanh, và luôn biết bản nào đang ở đâu.

Một binary duy nhất, không cần cài Node/Python/runtime gì. Giao diện được nhúng sẵn trong binary và chỉ phục vụ trên `localhost`.

## Cài lên máy

```powershell
.\build.ps1        # chạy test rồi build vào .\dist
.\install.ps1      # cài cho người dùng hiện tại + tạo shortcut Start Menu
```

Sau đó mở từ Start Menu bằng cách gõ `DIMA`. Ứng dụng mở ra **cửa sổ riêng**, có icon riêng trên taskbar, không thanh địa chỉ, không lẫn với các tab trình duyệt khác, và **không kèm cửa sổ terminal nào**.

Đóng cửa sổ là ứng dụng thoát hẳn và trả lại cổng — trừ khi còn build đang chạy, lúc đó nó chờ build xong rồi mới thôi.

| Lệnh | Tác dụng |
|---|---|
| `.\install.ps1` | cài, tạo shortcut Start Menu |
| `.\install.ps1 -Desktop` | thêm shortcut ngoài Desktop |
| `.\install.ps1 -AddToPath` | gõ được lệnh `dima` trong terminal |
| `.\install.ps1 -Uninstall` | gỡ ra (dữ liệu trong `~\.dima` vẫn giữ) |

Không cần quyền quản trị: mọi thứ nằm trong `%LOCALAPPDATA%\Programs\DIMA`.

Trên macOS/Linux thì chạy `./build.sh` rồi chạy thẳng binary trong `dist/`.

Muốn trên Mac có app riêng (icon, bấm từ Launchpad, bỏ vào `/Applications`) thay vì chạy binary trần, dùng `./build-mac-app.sh` — build universal binary (Intel + Apple Silicon), đóng gói thành `DIMA.app`, convert `ui/icon.svg` thành icon, và ký ad-hoc. Thêm cờ `--install` để copy thẳng vào `/Applications`.

## Chạy trực tiếp

```bash
# Windows
dist\dima-0.2.0-windows-amd64.exe

# macOS (Apple Silicon) / Linux
chmod +x dist/dima-0.2.0-darwin-arm64
./dist/dima-0.2.0-darwin-arm64
```

| Cờ | Mặc định | Ý nghĩa |
|---|---|---|
| `-port` | `7788` | Cổng lắng nghe. Không truyền thì bận cổng nào tự nhảy cổng đó |
| `-data` | `~/.dima` | Thư mục lưu config, lịch sử, log |
| `-no-open` | tắt | Không tự mở giao diện |
| `-tab` | tắt | Mở trong tab trình duyệt thường thay vì cửa sổ riêng |

Không phải lo chuyện cổng. Nếu 7788 đang bận, DIMA tự dò tiếp 7789, 7790… cho tới khi tìm được cổng trống, không báo lỗi gì. Còn nếu cổng đó đang do **một bản DIMA khác** giữ, nó không mở thêm bản thứ hai mà đưa cửa sổ của bản đang chạy lên — bấm shortcut mấy lần cũng chỉ có một ứng dụng.

Chỉ khi bạn tự truyền `-port` thì con số đó mới là bắt buộc, và lúc đó cổng bận mới bị báo lỗi.

Yêu cầu duy nhất: lệnh `docker` nằm trong `PATH`. Có thêm `docker buildx` thì thao tác promote chạy thẳng trên registry, không phải kéo image về máy. Có thêm `git` thì mỗi bản build được ghi kèm nhánh và commit.

Cửa sổ riêng dựa vào Edge, Chrome hoặc Brave có sẵn trên máy (chế độ `--app`, hồ sơ riêng trong `~/.dima/window`). Không có trình duyệt nào trong số đó thì ứng dụng tự lùi về mở tab bình thường.

## Cách tổ chức

```
Nhóm                       chỉ là cái nhãn để gom dự án cho đỡ rối
└── Dự án                  giữ những gì các môi trường dùng chung
    └── Môi trường         chỉ khai những gì khác với dự án
```

**Nhóm** là một chuỗi tên trên mỗi dự án, không phải một thực thể riêng. Dự án nào để trống thì nằm ở mục "Chưa phân nhóm". Đổi nhóm là sửa một ô chữ; nhóm tự biến mất khi không còn dự án nào mang tên đó, không có nhóm rỗng phải đi dọn. Ở cột trái, nhóm gấp/mở được, và trạng thái gấp được nhớ lại cho lần mở sau.

Khi bạn chọn thư mục `D:\work\khach-A\api`, ứng dụng điền sẵn nhóm là `khach-A`. Đó chỉ là gợi ý ban đầu — giá trị được lưu lại chứ không suy ra từ đường dẫn, nên sau này bạn chuyển thư mục đi đâu thì dự án vẫn ở nguyên nhóm cũ, và hai dự án nằm ở hai ổ đĩa khác nhau vẫn gom chung nhóm được.

### Ba cách thêm dự án

Cột trái có ba lối vào, mỗi cái làm đúng một việc:

| | Làm gì |
|---|---|
| **+ Tạo mới** | Tạo một dự án trống, bạn tự khai cài đặt |
| **Quét thư mục…** | Chọn một thư mục cha, tìm các thư mục con có Dockerfile |
| **Quét image…** | Đọc các image đã có sẵn trong Docker trên máy này |

**Quét thư mục** đi sâu tối đa ba tầng — đủ cho cả kiểu để phẳng nhiều repo cạnh nhau lẫn kiểu `apps/<tên>`. Thấy thư mục nào build được là dừng, không chui tiếp vào trong nó; `node_modules`, `vendor`, `dist`, `.git` và các thư mục ẩn bị bỏ qua. Mỗi dự án được tạo sẵn hai môi trường Staging và Production.

**Quét image** đọc `docker images` và gom theo repository, mỗi repository thành một dự án. Nó tách sẵn registry khỏi tên image (`harbor.tech/mm/web` → registry `harbor.tech`, image `mm/web`), và **ghi luôn các tag hiện có vào bảng version** kèm đúng thời điểm tạo image — nếu không thì nhập xong dự án vẫn hiện "chưa có bản nào" trong khi image đang nằm ngay đó. Tag phải chứa chữ số mới được coi là phiên bản, nên `latest`, `cache`, `stable` bị bỏ. Tag có tiền tố kiểu `prod-` / `staging-` thì môi trường tương ứng được tạo sẵn; không có tiền tố thì dự án có một môi trường tên "Mặc định".

Những bản đọc vào mang nhãn **đọc vào** để phân biệt với bản do DIMA build. Chúng chưa có digest trên registry nên không promote được cho tới khi bạn push.

Cả hai màn quét đều đánh dấu "đã có" và khóa những thứ đã nhập rồi, nên quét lại bao nhiêu lần cũng không tạo trùng.

Riêng màn quét image tích sẵn các repository có **tên miền registry riêng** (`harbor.tech/...`) và để trống các image Docker Hub, vì repo trên registry riêng gần như luôn là của bạn. Đây chỉ là phỏng đoán: một image công khai tải từ registry khác Docker Hub, ví dụ `codeberg.org/forgejo/forgejo`, cũng sẽ bị tích sẵn. Badge luôn hiện tên registry để bạn thấy mà bỏ tích.

**Dự án** giữ thư mục mã nguồn, registry, tên image, Dockerfile, platform, target stage, file `.env`, cờ thêm, build args và labels chung.

Việc đầu tiên khi tạo dự án là bấm **Chọn thư mục** để chỉ ra thư mục mã nguồn — đó là nơi lệnh `docker build` sẽ chạy. Hộp thoại là hộp thoại chọn thư mục của chính hệ điều hành, vì trang web trong trình duyệt không bao giờ được biết đường dẫn thật trên máy.

Ngay dưới ô đó, ứng dụng nói luôn nó thấy gì: thư mục có tồn tại không, có đúng Dockerfile bạn khai không, và nếu không thì trong thư mục đang có những Dockerfile nào.

Thư mục sai **không chặn việc lưu cấu hình** — lưu là ghi lại cả file, nên từ chối vì một dự án hỏng sẽ khóa luôn mọi thao tác khác, kể cả xóa chính dự án hỏng đó; thư mục cũng có thể biến mất ngoài ý muốn. Thay vào đó, dự án hỏng bị đánh dấu ngay trên màn hình của nó, và lệnh build bị từ chối kèm lý do cụ thể.

**Môi trường** (staging, production, …) chỉ khai phần khác biệt. Ô nào để trống thì lấy giá trị của dự án — form hiển thị sẵn `kế thừa: …` để bạn biết mình đang thừa hưởng cái gì.

Ba quy tắc kế thừa:

| Loại | Quy tắc |
|---|---|
| Các ô chữ (registry, image, Dockerfile, …) | môi trường khai thì môi trường thắng |
| Build args, labels | **trộn theo key** — dự án khai `APP_NAME` chung, môi trường chỉ cần khai `NODE_ENV` riêng |
| Cờ thêm | **nối** cờ của dự án rồi tới cờ của môi trường, vì cờ vốn cộng dồn |

Mỗi môi trường có một **tiền tố tag**. Version `1.2.3` với tiền tố `prod-` cho ra `registry/image:prod-1.2.3`. Nhờ tách version khỏi tag, một version nhìn thấy được trên mọi môi trường.

## Chức năng

**Tổng quan** — một màn hình cho biết bản nào đang nằm ở môi trường nào, gom theo nhóm, kèm digest rút gọn và thời điểm. Môi trường nào tụt lại sau bản mới nhất sẽ được đánh dấu "chậm hơn bản mới nhất N phiên bản".

**Lịch sử theo version** — bảng version × môi trường của từng dự án. Ô trống có sẵn nút để đưa version đó sang môi trường còn thiếu.

**Hai cách đưa version qua các môi trường**, khai ở từng dự án:

- `rebuild` — mỗi môi trường build lại từ mã nguồn với build args của nó. Hợp với ứng dụng nhúng biến lúc build.
- `promote` — build một lần ở môi trường nguồn, rồi gắn thêm tag cho chính image đó ở các môi trường khác. Digest giữ nguyên, nên bản lên production đúng là bản đã test. Ở chế độ này chỉ môi trường nguồn có nút Build; các môi trường khác chỉ nhận promote.

**Promote và rollback** — cùng một cơ chế. Promote một version cũ lên môi trường đang hỏng chính là rollback. Có `docker buildx` thì tag được gắn thẳng trên registry (không truyền dữ liệu, giữ được manifest đa kiến trúc); không có thì lùi về `pull` → `tag` → `push`.

**Build nhanh** — hộp thoại hỏi version, gợi ý sẵn bản patch/minor kế tiếp dựa trên version mới nhất **của dự án**, kèm một gợi ý theo dấu thời gian. Có xem trước ref sẽ tạo ra. Tick chọn để push luôn sau khi build.

**Lệnh build riêng** — thư mục nào có sẵn script build thì khai lệnh của nó vào ô "Lệnh build riêng", DIMA chạy lệnh đó thay cho `docker build`. Để trống thì mọi thứ như cũ. Môi trường ghi đè được lệnh của dự án, như mọi trường khác.

Lệnh nhận các biến trong ngoặc nhọn:

| Biến | Là gì |
|---|---|
| `{version}` | version của dự án, ví dụ `1.2.3` |
| `{tag}` | tag thật trên registry, gồm cả tiền tố của môi trường |
| `{ref}` | tên image đầy đủ kèm tag |
| `{repo}` · `{registry}` · `{image}` | các phần của tên image |
| `{env}` · `{project}` | tên môi trường, tên dự án |
| `{context}` · `{dockerfile}` | thư mục mã nguồn, tên Dockerfile |
| `{envfile}` | đường dẫn file `.env` đã khai ở môi trường (ô "File .env"); để trống thì mặc định `.env` |

Ví dụ: `./build.ps1 -Version {version} -Ref {ref}`. Gõ sai tên biến thì nó được giữ nguyên trong lệnh chứ không âm thầm thành rỗng — sai sót phải nhìn thấy được.

Lệnh chạy qua shell của hệ điều hành, tại thư mục mã nguồn: PowerShell trên Windows (vì `cmd` không chạy được `.ps1` và không hiểu `./`), `sh` trên macOS/Linux. Nhờ đó `&&`, ống dẫn và dấu nháy hành xử đúng như khi bạn gõ trong terminal.

**Lệnh của bạn cần gắn tag image thành `{ref}`.** Push, digest, promote và bảng version đều dựa vào cái tên đó. Chạy xong mà DIMA không thấy image ấy trên máy, nó ghi một dòng nhắc vào log chứ không coi là build hỏng — script của bạn có thể đã tự push rồi. Khi dùng lệnh riêng, DIMA cũng không đòi phải có Dockerfile trong thư mục nữa.

**Nhánh git của từng bản** — mỗi lần build, DIMA đọc thư mục mã nguồn và ghi lại nhánh, commit rút gọn, và việc lúc đó có thay đổi chưa commit hay không. Lịch sử hiện thẳng tên nhánh bên cạnh version, nên không còn phải đoán bản nào thuộc nhánh nào.

Bản build khi thư mục còn sửa dở được tô cam kèm dấu `*`: image đó **không khớp với commit nào**, sau này không dựng lại được từ repo. Đây là thứ dễ bỏ sót nhất và cũng khó truy nhất về sau.

Quan trọng hơn việc ghi lại: hộp thoại Build **hiện nhánh đang đứng trước khi bạn bấm**, và tô cảnh báo nếu nhánh đó khác với lần build trước của chính môi trường này. Ghi lại chỉ giúp truy ngược sau khi đã lỡ; hiện trước mới ngăn được việc build nhầm.

Bản promote thừa hưởng nhánh và commit của bản gốc, vì nó ship đúng image đó chứ không build lại. Thư mục không phải git repo, hoặc máy không có `git`, thì phần này im lặng bỏ qua — nó là thông tin thêm, không bao giờ là lý do chặn build.

**Nhật ký trực tiếp** — log docker chảy về giao diện trong lúc build hoặc promote, có nút dừng giữa chừng.

**Lệnh pull** — mở chi tiết một bản sẽ thấy sẵn lệnh `docker pull`, dùng digest nếu đã push (bất biến, đúng bản đó) hoặc tag nếu chưa. Nút Chép bên cạnh.

**Bản lưu cấu hình** — toàn bộ cài đặt nằm trong `~/.dima/config.json`. Mỗi lần lưu, bản cũ được cất vào `~/.dima/versions/config-<thời gian>.json`. Nút "Bản lưu" cho xem lại và khôi phục bất kỳ bản nào.

## Dữ liệu

```
~/.dima/
  config.json                    dự án, môi trường
  history.json                   lịch sử build và promote
  versions/config-*.json         các bản cấu hình cũ
  logs/<build-id>.log            log từng lần build
  window/                        hồ sơ trình duyệt của cửa sổ ứng dụng
```

Tất cả là JSON thường, sửa tay hoặc đưa vào Git đều được. File có BOM (Notepad trên Windows hay thêm) vẫn đọc được.

### Nâng cấp từ 0.1.x

Lần đầu mở bản 0.2.0, `config.json` cũ được chuyển sang cấu trúc mới:

- Bản cũ được cất vào `versions/` trước khi đụng tới, khôi phục lại được bất cứ lúc nào.
- Toàn bộ môi trường cũ gom vào một dự án tên "Dự án của tôi", chế độ `rebuild` — đúng hành vi cũ.
- Cài đặt nào **mọi môi trường đều giống nhau** được nâng lên dự án và xóa ở môi trường. Giá trị hiệu lực không đổi, nhưng bạn thấy ngay chỗ nào là dùng chung.
- Lịch sử build cũ được gắn version (bằng chính tag cũ) và gắn vào dự án.

## Mã nguồn

Toàn bộ mã nằm ngay ở thư mục gốc, một dự án Go duy nhất. Cần Go 1.22 trở lên. Không có dependency ngoài stdlib, nên không cần mạng khi build.

```bash
go test ./...              # 40 test
go build -o dima .         # build cho máy hiện tại
./build.sh                 # build cả 6 nền tảng vào ./dist
```

```powershell
.\build.ps1                # bản PowerShell, có kèm gói mã nguồn
.\build.ps1 -Only windows  # chỉ build cho Windows
```

| File | Nội dung |
|---|---|
| `main.go` | điểm vào, HTTP server, chọn cổng, nhúng UI, mở cửa sổ ứng dụng |
| `console_windows.go` | nối lại output khi chạy từ terminal, hộp thoại báo lỗi khởi động |
| `console_other.go` | bản rỗng cho macOS/Linux |
| `store.go` | mô hình dữ liệu, quy tắc kế thừa, đọc/ghi JSON, migration |
| `builder.go` | chạy `docker build` / `push` / `promote`, đọc `docker images`, gom log, hủy |
| `images.go` | gom image theo repository, tách registry, nhận diện tag phiên bản |
| `git.go` | đọc nhánh, commit và trạng thái sạch/bẩn của thư mục mã nguồn |
| `command.go` | danh sách biến và việc thay biến cho lệnh build riêng |
| `api.go` | các endpoint HTTP |
| `store_test.go`, `builder_test.go`, `images_test.go`, `git_test.go` | test cho kế thừa, migration, lệnh promote, phân loại image và đọc git |
| `ui/index.html` | toàn bộ giao diện, không framework |
| `picker_windows.go` | hộp thoại chọn thư mục của Windows, mở qua powershell (xem ghi chú ở Hạn chế) |
| `picker_other.go` | bản dùng osascript trên macOS, zenity/kdialog trên Linux |
| `ui/icon.svg` | icon dùng cho cửa sổ và cho shortcut |
| `build.sh`, `build.ps1` | build ra `dist/` |
| `install.ps1` | cài thành ứng dụng desktop trên Windows |

Quy tắc kế thừa nằm gọn trong `Project.Effective`; mọi thành phần khác chỉ làm việc với môi trường đã resolve.

## Bảo mật

Server chỉ bind vào `127.0.0.1` và từ chối request có `Host` không phải loopback, để chặn tấn công DNS rebinding — cần thiết vì ứng dụng này chạy được lệnh `docker`.

Không có xác thực người dùng: bất kỳ ai dùng được máy này đều chạy được ứng dụng. Với máy cá nhân thì đủ; đừng chạy trên server dùng chung.

Thông tin đăng nhập registry không nằm trong ứng dụng. Đăng nhập một lần bằng `docker login` ở terminal, DIMA dùng lại credential đó.

## Hạn chế đã biết

- Build chạy trên chính máy đang mở ứng dụng, không phải CI. Máy tắt là không build được.
- Chưa có phân quyền hay nhật ký kiểm toán. Nếu cần chứng minh ai đưa bản nào lên production, dùng CI đúng nghĩa.
- Chưa đọc trực tiếp registry, nên danh sách version là những bản DIMA đã build hoặc promote, không phải mọi tag có trên registry. Các cảnh báo trùng version cũng chỉ dựa trên lịch sử cục bộ.
- Mỗi lần chỉ nên chạy một build cho một môi trường; ứng dụng không chặn việc chạy song song.
- Ở chế độ `promote`, hai môi trường phải có tiền tố tag khác nhau. Trùng tiền tố thì tag đích trùng tag nguồn và ứng dụng sẽ từ chối, vì thao tác đó không có tác dụng gì.
- File `.exe` chưa có icon nhúng bên trong; icon hiện qua shortcut và qua cửa sổ ứng dụng. Muốn nhúng thẳng vào exe thì cần thêm công cụ sinh resource, và sẽ mất tính chất "chỉ stdlib".
- Binary chưa được ký số. Trên máy bật **Smart App Control**, Windows có thể từ chối chạy một bản build, báo "An Application Control policy has blocked this file".

  SAC là danh sách cho phép: nó chỉ chạy file có chữ ký số được tin, hoặc file đã có tiếng trong đám mây của Microsoft vì nhiều máy khác chạy rồi. Một bản bạn tự build không có cả hai, nên bị chặn vì **lạ**, không phải vì làm gì sai. Không có tính năng nào trong DIMA gây ra chuyện này.

  Phán quyết được nhớ **theo từng hash file**. Mà Go build vốn tái lập được: cùng nguồn thì ra đúng cùng file, nên build lại y nguyên sẽ dính lại đúng phán quyết cũ. Vì thế `build.ps1` và `build.sh` nhúng một **dấu build** theo thời gian, khiến mỗi lần build ra một file khác và được xét lại từ đầu. Việc này không làm binary trở nên đáng tin — SAC vẫn xét từng bản; chỉ là một bản lỡ bị từ chối không khóa luôn mọi bản sau.

  `install.ps1` thử chạy bản mới trước khi thay bản đang dùng. Bị chặn thì nó dừng lại và giữ nguyên bản cũ, chứ không để bạn mất một ứng dụng đang chạy được. Gặp vậy thì chạy `.\build.ps1` lần nữa rồi cài lại.

  Muốn hết hẳn thì phải ký số binary bằng chứng thư của một CA mà Microsoft tin. Tắt Smart App Control cũng hết, nhưng **tắt rồi không bật lại được** nếu không cài lại Windows.


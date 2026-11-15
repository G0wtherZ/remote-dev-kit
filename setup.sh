#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$(uname -s)" != Darwin ]; then
  echo '此本机安装入口面向 macOS；远端 Linux 由 setup 自动识别。' >&2
  exit 1
fi
target="$HOME/.local/share/remote-dev-kit/app"
if [ ! -f "$here/SHA256SUMS" ] || [ ! -d "$here/assets" ] || [ ! -f "$here/rdev" ]; then
  echo '请从完整的对应平台安装包运行 setup.sh，不要单独复制脚本。' >&2
  exit 1
fi
(cd "$here" && shasum -a 256 -c SHA256SUMS >/dev/null)
if [ -e "$target/rdev" ]; then
  if cmp -s "$here/rdev" "$target/rdev"; then
    exec "$target/rdev" setup "$@"
  fi
  echo "已有不同版本，未覆盖。可在新包目录运行 ./rdev setup 继续使用现有配置。" >&2
  echo "安装目录：$target；先 rdev uninstall 停止服务并保留数据，再按 README 的升级说明处理。" >&2
  exit 1
fi
mkdir -p "$target" "$HOME/.local/bin"
chmod 700 "$HOME/.local/share/remote-dev-kit"
cp "$here/rdev" "$here/README.md" "$here/LICENSE" "$here/THIRD_PARTY_NOTICES.md" "$here/SHA256SUMS" "$target/"
cp -R "$here/assets" "$here/licenses" "$target/"
cp -R "$here/docs" "$target/"
chmod +x "$target/rdev"
if [ -e "$HOME/.local/bin/rdev" ] || [ -L "$HOME/.local/bin/rdev" ]; then
  echo '已有同名 rdev 命令，未覆盖。请使用下方完整路径。'
else
  ln -s "$target/rdev" "$HOME/.local/bin/rdev"
fi
echo "命令路径：$HOME/.local/bin/rdev（若不在 PATH，可用完整路径）"
case "${SHELL:-}" in
  */zsh) profile="$HOME/.zprofile" ;;
  *) profile="$HOME/.bash_profile" ;;
esac
if ! grep -Fq '# remote-dev-kit PATH' "$profile" 2>/dev/null; then
  printf '\n# remote-dev-kit PATH\nexport PATH="$HOME/.local/bin:$PATH"\n# end remote-dev-kit PATH\n' >> "$profile"
fi
export PATH="$HOME/.local/bin:$PATH"
echo '当前向导可直接使用 rdev；其他终端窗口重新打开后生效。'
exec "$target/rdev" setup "$@"

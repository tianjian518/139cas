#!/bin/sh

umask ${UMASK}

# ---- 端口适配（兼容面板 / 单端口环境）----
# 优先使用 OpenList 原生变量 OPENLIST_HTTP_PORT；
# 否则尝试读取面板注入的 SERVER_PORT / PORT 并映射过去。
if [ -n "$OPENLIST_HTTP_PORT" ]; then
  echo "🔌 OPENLIST_HTTP_PORT=$OPENLIST_HTTP_PORT"
elif [ -n "$SERVER_PORT" ]; then
  export OPENLIST_HTTP_PORT="$SERVER_PORT"
  echo "🔌 SERVER_PORT=$SERVER_PORT -> OPENLIST_HTTP_PORT"
elif [ -n "$PORT" ]; then
  export OPENLIST_HTTP_PORT="$PORT"
  echo "🔌 PORT=$PORT -> OPENLIST_HTTP_PORT"
else
  export OPENLIST_HTTP_PORT="5244"
  echo "⚠️  未检测到端口变量，使用默认 5244"
fi

# 单端口环境（如只给一个 HTTP 端口的面板）设 OPENLIST_DISABLE_HTTPS=1 关闭 HTTPS，
# 避免无效监听占用第二个端口。
if [ "$OPENLIST_DISABLE_HTTPS" = "1" ]; then
  export OPENLIST_HTTPS_PORT="-1"
fi

if [ "$1" = "version" ]; then
  ./openlist version
else
  # Check file of /opt/openlist/data permissions for current user
  # 检查当前用户是否有当前目录的写和执行权限
  if [ -d ./data ]; then
    if ! [ -w ./data ] || ! [ -x ./data ]; then
  cat <<EOF
Error: Current user does not have write and/or execute permissions for the ./data directory: $(pwd)/data
Please visit https://doc.oplist.org/guide/installation/docker#for-version-after-v4-1-0 for more information.
错误：当前用户没有 ./data 目录（$(pwd)/data）的写和/或执行权限。
请访问 https://doc.oplist.org/guide/installation/docker#v4-1-0-%E4%BB%A5%E5%90%8E%E7%89%88%E6%9C%AC 获取更多信息。
Exiting...
EOF
      exit 1
    fi
  fi

  # Define the target directory path for aria2 service
  ARIA2_DIR="/opt/service/start/aria2"
  if [ "$RUN_ARIA2" = "true" ]; then
    # If aria2 should run and target directory doesn't exist, copy it
    if [ ! -d "$ARIA2_DIR" ]; then
      mkdir -p "$ARIA2_DIR"
      cp -r /opt/service/stop/aria2/* "$ARIA2_DIR" 2>/dev/null
    fi
    runsvdir /opt/service/start &
  else
    # If aria2 should NOT run and target directory exists, remove it
    if [ -d "$ARIA2_DIR" ]; then
      rm -rf "$ARIA2_DIR"
    fi
  fi
  exec ./openlist server --no-prefix
fi

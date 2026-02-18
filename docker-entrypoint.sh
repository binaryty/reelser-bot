#!/bin/sh
set -e

# Обновляем yt-dlp до последней версии при каждом запуске
echo "=========================================="
echo "Updating yt-dlp to latest version..."
echo "=========================================="

# Пробуем обновить через pip
pip3 install --no-cache-dir --break-system-packages -U yt-dlp 2>/dev/null || \
pip3 install --no-cache-dir -U yt-dlp 2>/dev/null || \
echo "Warning: Could not update yt-dlp via pip"

# Проверяем версию
echo ""
echo "yt-dlp version:"
yt-dlp --version 2>/dev/null || echo "yt-dlp not found"
echo "=========================================="
echo ""

# Запускаем основное приложение
exec "$@"

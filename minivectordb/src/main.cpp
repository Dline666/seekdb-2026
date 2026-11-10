#include <charconv>
#include <csignal>
#include <cstdio>
#include <exception>
#include <filesystem>
#include <memory>
#include <string>
#include <string_view>
#include <system_error>

#ifndef _WIN32
#include <signal.h>
#else
#include <fcntl.h>
#include <io.h>
#endif

#ifdef MVDB_TIMING
#include <chrono>
#endif

#include "engine/engine.h"
#include "protocol/formatter.h"
#include "protocol/json_session.h"
#include "protocol/session.h"

namespace {

volatile std::sig_atomic_t g_stop = 0;

extern "C" void on_signal(int /*signum*/) { g_stop = 1; }

void install_signal_handlers() {
#ifdef _WIN32
  std::signal(SIGINT, on_signal);
  std::signal(SIGTERM, on_signal);
#else
  // 不设 SA_RESTART：被信号打断的 read 需要返回 EINTR，会话才能及时结束。
  struct sigaction action {};
  action.sa_handler = on_signal;
  sigemptyset(&action.sa_mask);
  action.sa_flags = 0;
  ::sigaction(SIGINT, &action, nullptr);
  ::sigaction(SIGTERM, &action, nullptr);
#endif
}

void print_usage(std::FILE* out) {
  std::fprintf(out,
               "usage: minivectordb [--data-dir PATH] [--protocol json|table|echo]\n"
               "                   [--buffer-pages N]\n"
               "  --data-dir PATH      数据目录，默认 /data\n"
               "  --protocol NAME      json（默认，评测协议）、table 或 echo（调试）\n"
               "  --buffer-pages N     缓冲池帧数，默认 2048（每帧 8 KiB）\n"
               "  --help               显示本说明\n");
}

bool parse_size(std::string_view text, std::size_t* out) {
  unsigned long long value = 0;
  auto const result =
      std::from_chars(text.data(), text.data() + text.size(), value);
  if (result.ec != std::errc() || result.ptr != text.data() + text.size() ||
      value == 0) {
    return false;
  }
  *out = static_cast<std::size_t>(value);
  return true;
}

}  // namespace

int main(int argc, char** argv) {
#ifdef _WIN32
  // 评测协议按字节比较，Windows 上也必须输出 LF。
  _setmode(_fileno(stdin), _O_BINARY);
  _setmode(_fileno(stdout), _O_BINARY);
#endif

  std::filesystem::path data_dir = "/data";
  std::string protocol = "json";
  std::size_t buffer_pages = 2048;

  for (int i = 1; i < argc; ++i) {
    std::string_view const arg = argv[i];
    if (arg == "--data-dir") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "minivectordb: missing value for --data-dir\n");
        return 2;
      }
      data_dir = argv[++i];
    } else if (arg == "--protocol") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "minivectordb: missing value for --protocol\n");
        return 2;
      }
      protocol = argv[++i];
      if (protocol != "json" && protocol != "table" && protocol != "echo") {
        std::fprintf(stderr, "minivectordb: unknown protocol '%s'\n",
                     protocol.c_str());
        return 2;
      }
    } else if (arg == "--buffer-pages") {
      if (i + 1 >= argc || !parse_size(argv[i + 1], &buffer_pages)) {
        std::fprintf(stderr, "minivectordb: invalid --buffer-pages value\n");
        return 2;
      }
      ++i;
    } else if (arg == "--help" || arg == "-h") {
      print_usage(stdout);
      return 0;
    } else {
      std::fprintf(stderr, "minivectordb: unknown argument: %s\n", argv[i]);
      print_usage(stderr);
      return 2;
    }
  }

  std::error_code ec;
  std::filesystem::create_directories(data_dir, ec);
  if (ec) {
    std::fprintf(stderr, "minivectordb: cannot create data dir %s: %s\n",
                 data_dir.string().c_str(), ec.message().c_str());
    return 1;
  }

  install_signal_handlers();

  std::unique_ptr<mvdb::Engine> engine;
  try {
    engine = std::make_unique<mvdb::Engine>(data_dir, buffer_pages);
  } catch (const std::exception& error) {
    std::fprintf(stderr, "minivectordb: %s\n", error.what());
    return 1;
  }

  std::fprintf(stderr, "minivectordb: ready (data-dir=%s, protocol=%s)\n",
               std::filesystem::absolute(data_dir).string().c_str(),
               protocol.c_str());

  int code = 0;
  if (protocol == "json") {
    // 评测握手：必须在等待第一条请求之前主动输出并 flush。
    std::fputs("{\"type\":\"ready\"}\n", stdout);
    std::fflush(stdout);
    mvdb::JsonSession session(*engine, stdin, stdout);
    code = session.run();
  } else {
    std::unique_ptr<mvdb::Formatter> formatter;
    if (protocol == "echo") {
      formatter = std::make_unique<mvdb::EchoFormatter>();
    } else {
      formatter = std::make_unique<mvdb::TabularFormatter>();
    }
    mvdb::Session session(*engine, *formatter, stdout);
    code = session.run();
  }
#ifdef MVDB_TIMING
  auto const shutdown_start = std::chrono::steady_clock::now();
#endif
  engine->shutdown();
#ifdef MVDB_TIMING
  std::fprintf(stderr, "timing: shutdown=%.3f\n",
               std::chrono::duration<double>(std::chrono::steady_clock::now() -
                                             shutdown_start)
                   .count());
#endif

  if (g_stop != 0) {
    std::fprintf(stderr, "minivectordb: shutting down (signal received)\n");
  }
  std::fprintf(stderr, "minivectordb: exit, statements=%llu\n",
               static_cast<unsigned long long>(engine->executed_statements()));
  return code;
}

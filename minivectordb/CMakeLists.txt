cmake_minimum_required(VERSION 3.20)
project(minivectordb LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

option(MINIVECTORDB_SANITIZE "用 ASan+UBSan 构建（仅本地测试）" OFF)
option(MINIVECTORDB_WERROR "把警告当错误" OFF)

add_executable(minivectordb
  src/main.cpp
  src/common/schema.cpp
  src/common/value.cpp
  src/engine/engine.cpp
  src/exec/apply.cpp
  src/exec/catalog.cpp
  src/exec/executor.cpp
  src/protocol/formatter.cpp
  src/protocol/json.cpp
  src/protocol/json_session.cpp
  src/protocol/session.cpp
  src/protocol/statement_reader.cpp
  src/sql/lexer.cpp
  src/sql/parser.cpp
  src/storage/buffer_pool.cpp
  src/storage/encoding.cpp
  src/storage/file_manager.cpp
  src/storage/heap_table.cpp
  src/storage/page.cpp
  src/storage/persistence.cpp
  src/storage/record.cpp
  src/storage/schema_codec.cpp
  src/storage/wal.cpp
)

target_include_directories(minivectordb PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/src)

target_compile_options(minivectordb PRIVATE
  -Wall -Wextra -Wpedantic -Wshadow -Wnon-virtual-dtor -Wold-style-cast
)

# GCC 对含 double 的 std::variant 有 -Wmaybe-uninitialized 误报（union 存储）。
if(CMAKE_CXX_COMPILER_ID STREQUAL "GNU")
  target_compile_options(minivectordb PRIVATE -Wno-maybe-uninitialized)
endif()

if(MINIVECTORDB_WERROR)
  target_compile_options(minivectordb PRIVATE -Werror)
endif()

if(MINIVECTORDB_SANITIZE)
  target_compile_options(minivectordb PRIVATE
    -fsanitize=address,undefined -fno-omit-frame-pointer -g)
  target_link_options(minivectordb PRIVATE -fsanitize=address,undefined)
endif()

if(CMAKE_BUILD_TYPE STREQUAL "Release")
  target_compile_options(minivectordb PRIVATE -O2)
endif()

# 运行镜像只带 libc，缩小体积、加快启动。
if(CMAKE_CXX_COMPILER_ID STREQUAL "GNU" AND NOT WIN32)
  target_link_options(minivectordb PRIVATE -static-libstdc++ -static-libgcc)
endif()

install(TARGETS minivectordb RUNTIME DESTINATION bin)

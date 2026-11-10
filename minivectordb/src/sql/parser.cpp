#include "sql/parser.h"

#include <cstdint>
#include <string>
#include <utility>
#include <vector>

#include "common/error.h"
#include "sql/lexer.h"

namespace mvdb::sql {
namespace {

class Parser {
 public:
  explicit Parser(std::vector<Token> tokens) : tokens_(std::move(tokens)) {}

  Statement parse_statement() {
    if (is_kw(peek(), "create")) return parse_create();
    if (accept_kw("insert")) return parse_insert();
    if (accept_kw("select")) return parse_select();
    if (accept_kw("update")) return parse_update();
    if (accept_kw("delete")) return parse_delete();
    if (accept_kw("begin")) {
      expect_end();
      return TransactionStmt{TransactionStmt::Kind::kBegin};
    }
    if (accept_kw("commit")) {
      expect_end();
      return TransactionStmt{TransactionStmt::Kind::kCommit};
    }
    if (accept_kw("rollback")) {
      expect_end();
      return TransactionStmt{TransactionStmt::Kind::kRollback};
    }
    if (peek().kind == TokenKind::kEnd) throw SqlError("empty statement");
    throw SqlError("unsupported statement '" + peek().text + "'");
  }

 private:
  const Token& peek(std::size_t ahead = 0) const {
    std::size_t const idx = pos_ + ahead;
    return idx < tokens_.size() ? tokens_[idx] : tokens_.back();
  }

  bool at_end() const { return peek().kind == TokenKind::kEnd; }

  bool is_kw(const Token& token, std::string_view keyword) const {
    return token.kind == TokenKind::kIdent && equal_ci(token.text, keyword);
  }

  bool accept_kw(std::string_view keyword) {
    if (!is_kw(peek(), keyword)) return false;
    ++pos_;
    return true;
  }

  void expect_kw(std::string_view keyword) {
    if (!accept_kw(keyword)) {
      error("expected keyword " + std::string(keyword));
    }
  }

  std::string expect_ident() {
    if (peek().kind != TokenKind::kIdent) error("expected identifier");
    return tokens_[pos_++].text;
  }

  bool accept_symbol(std::string_view symbol) {
    if (peek().kind == TokenKind::kSymbol && peek().text == symbol) {
      ++pos_;
      return true;
    }
    return false;
  }

  void expect_symbol(std::string_view symbol) {
    if (!accept_symbol(symbol)) {
      error("expected '" + std::string(symbol) + "'");
    }
  }

  std::int64_t expect_number(std::string_view what) {
    if (peek().kind != TokenKind::kNumber) {
      error("expected " + std::string(what));
    }
    return tokens_[pos_++].number;
  }

  void expect_end() {
    if (!at_end()) error("unexpected token");
  }

  [[noreturn]] void error(const std::string& message) const {
    Token const& token = peek();
    std::string const near = token.kind == TokenKind::kEnd
                                 ? "<end of statement>"
                                 : "'" + token.text + "'";
    throw SqlError(message + " near " + near + " at position " +
                   std::to_string(token.pos));
  }

  Statement parse_create() {
    ++pos_;  // CREATE
    if (accept_kw("vector")) {
      expect_kw("index");
      CreateVectorIndexStmt stmt;
      stmt.index = expect_ident();
      expect_kw("on");
      stmt.table = expect_ident();
      expect_symbol("(");
      stmt.column = expect_ident();
      expect_symbol(")");
      expect_end();
      return stmt;
    }
    if (accept_kw("table")) {
      CreateTableStmt stmt;
      stmt.table = expect_ident();
      expect_symbol("(");
      stmt.columns.push_back(parse_column_def());
      std::string table_pk;
      while (accept_symbol(",")) {
        if (accept_kw("primary")) {
          expect_kw("key");
          expect_symbol("(");
          table_pk = expect_ident();
          expect_symbol(")");
          continue;
        }
        stmt.columns.push_back(parse_column_def());
      }
      expect_symbol(")");
      expect_end();
      if (!table_pk.empty()) {
        bool found = false;
        for (ColumnDef& column : stmt.columns) {
          if (equal_ci(column.name, table_pk)) {
            column.primary_key = true;
            found = true;
          }
        }
        if (!found) {
          throw SqlError("unknown column '" + table_pk + "' in primary key");
        }
      }
      return stmt;
    }
    throw SqlError("unsupported statement 'CREATE " + peek().text + "'");
  }

  ColumnDef parse_column_def() {
    ColumnDef def;
    def.name = expect_ident();
    def.type = parse_column_type();
    if (accept_kw("primary")) {
      expect_kw("key");
      def.primary_key = true;
    }
    return def;
  }

  ColumnType parse_column_type() {
    std::string const keyword = expect_ident();
    if (equal_ci(keyword, "bigint")) {
      return ColumnType{TypeKind::kBigInt, 0};
    }
    if (equal_ci(keyword, "varchar") || equal_ci(keyword, "vector")) {
      expect_symbol("(");
      std::int64_t const size = expect_number("size");
      expect_symbol(")");
      if (size <= 0) throw SqlError("invalid size for " + keyword);
      ColumnType type;
      type.kind =
          equal_ci(keyword, "vector") ? TypeKind::kVector : TypeKind::kVarchar;
      type.size = static_cast<std::uint32_t>(size);
      return type;
    }
    throw SqlError("unsupported column type '" + keyword + "'");
  }

  Literal parse_literal() {
    if (accept_symbol("-")) {
      if (peek().kind != TokenKind::kNumber) {
        error("expected number after '-'");
      }
      Literal lit;
      lit.kind = Literal::Kind::kInt;
      lit.int_value = -tokens_[pos_++].number;
      return lit;
    }
    Token const& token = peek();
    if (token.kind == TokenKind::kNumber) {
      Literal lit;
      lit.kind = Literal::Kind::kInt;
      lit.int_value = token.number;
      ++pos_;
      return lit;
    }
    if (token.kind == TokenKind::kString) {
      Literal lit;
      lit.kind = Literal::Kind::kString;
      lit.text = token.text;
      ++pos_;
      return lit;
    }
    if (token.kind == TokenKind::kVector) {
      Literal lit;
      lit.kind = Literal::Kind::kVector;
      lit.vector = token.vector;
      ++pos_;
      return lit;
    }
    if (is_kw(token, "null")) {
      ++pos_;
      Literal lit;
      lit.kind = Literal::Kind::kNull;
      return lit;
    }
    error("expected literal value");
  }

  Literal parse_query_vector() {
    Token const& token = peek();
    if (token.kind == TokenKind::kString) {
      Literal lit;
      lit.kind = Literal::Kind::kString;
      lit.text = token.text;
      ++pos_;
      return lit;
    }
    if (token.kind == TokenKind::kVector) {
      Literal lit;
      lit.kind = Literal::Kind::kVector;
      lit.vector = token.vector;
      ++pos_;
      return lit;
    }
    error("expected vector literal");
  }

  bool at_function_call(std::string_view name) const {
    return is_kw(peek(), name) && peek(1).kind == TokenKind::kSymbol &&
           peek(1).text == "(";
  }

  Statement parse_insert() {
    InsertStmt stmt;
    expect_kw("into");
    stmt.table = expect_ident();
    if (accept_symbol("(")) {
      stmt.columns.push_back(expect_ident());
      while (accept_symbol(",")) stmt.columns.push_back(expect_ident());
      expect_symbol(")");
    }
    expect_kw("values");
    do {
      std::vector<Literal> row;
      expect_symbol("(");
      row.push_back(parse_literal());
      while (accept_symbol(",")) row.push_back(parse_literal());
      expect_symbol(")");
      stmt.rows.push_back(std::move(row));
    } while (accept_symbol(","));
    expect_end();
    return stmt;
  }

  Statement parse_select() {
    SelectStmt stmt;
    do {
      stmt.items.push_back(parse_select_item());
    } while (accept_symbol(","));
    expect_kw("from");
    stmt.table = expect_ident();
    if (accept_kw("where")) {
      stmt.has_where = true;
      stmt.where = parse_condition();
    }
    if (accept_kw("order")) {
      expect_kw("by");
      do {
        stmt.order_by.push_back(parse_order_item());
      } while (accept_symbol(","));
    }
    if (accept_kw("limit")) {
      stmt.has_limit = true;
      stmt.limit = expect_number("limit");
    }
    expect_end();
    return stmt;
  }

  SelectItem parse_select_item() {
    SelectItem item;
    if (accept_symbol("*")) {
      item.star = true;
      return item;
    }
    if (at_function_call("l2_distance")) {
      ++pos_;
      expect_symbol("(");
      item.is_distance = true;
      item.column = expect_ident();
      expect_symbol(",");
      item.query = parse_query_vector();
      expect_symbol(")");
      if (accept_kw("as")) item.alias = expect_ident();
      return item;
    }
    if (peek().kind == TokenKind::kIdent &&
        peek(1).kind == TokenKind::kSymbol && peek(1).text == "(") {
      throw SqlError("unsupported function '" + peek().text + "'");
    }
    item.column = expect_ident();
    if (accept_kw("as")) item.alias = expect_ident();
    return item;
  }

  OrderItem parse_order_item() {
    OrderItem item;
    if (at_function_call("l2_distance")) {
      ++pos_;
      expect_symbol("(");
      item.is_distance = true;
      item.column = expect_ident();
      expect_symbol(",");
      item.query = parse_query_vector();
      expect_symbol(")");
    } else {
      item.column = expect_ident();
    }
    if (accept_kw("desc")) {
      item.descending = true;
    } else {
      accept_kw("asc");
    }
    return item;
  }

  Condition parse_condition() {
    Condition cond;
    parse_condition_into(cond);
    return cond;
  }

  void parse_condition_into(Condition& cond) {
    for (;;) {
      if (accept_symbol("(")) {
        parse_condition_into(cond);
        expect_symbol(")");
      } else {
        cond.terms.push_back(parse_term());
      }
      if (!accept_kw("and")) break;
    }
  }

  Condition::Term parse_term() {
    bool left_is_column = false;
    std::string left_column;
    Literal left_literal;
    parse_operand(&left_is_column, &left_column, &left_literal);

    CompareOp const op = parse_compare_op();

    bool right_is_column = false;
    std::string right_column;
    Literal right_literal;
    parse_operand(&right_is_column, &right_column, &right_literal);

    if (left_is_column == right_is_column) {
      throw SqlError(
          "each comparison must reference exactly one column");
    }
    Condition::Term term;
    term.op = op;
    if (left_is_column) {
      term.column = left_column;
      term.literal = right_literal;
      term.column_on_left = true;
    } else {
      term.column = right_column;
      term.literal = left_literal;
      term.column_on_left = false;
    }
    return term;
  }

  void parse_operand(bool* is_column, std::string* column, Literal* literal) {
    Token const& token = peek();
    if (token.kind == TokenKind::kIdent) {
      if (peek(1).kind == TokenKind::kSymbol && peek(1).text == "(") {
        throw SqlError("unsupported function '" + token.text +
                       "' in WHERE clause");
      }
      *is_column = true;
      *column = token.text;
      ++pos_;
      return;
    }
    *is_column = false;
    *literal = parse_literal();
  }

  CompareOp parse_compare_op() {
    if (accept_symbol("=")) return CompareOp::kEq;
    if (accept_symbol("<=")) return CompareOp::kLe;
    if (accept_symbol(">=")) return CompareOp::kGe;
    if (accept_symbol("<")) return CompareOp::kLt;
    if (accept_symbol(">")) return CompareOp::kGt;
    error("expected comparison operator");
  }

  Statement parse_update() {
    UpdateStmt stmt;
    stmt.table = expect_ident();
    expect_kw("set");
    do {
      std::string column = expect_ident();
      expect_symbol("=");
      stmt.assignments.emplace_back(std::move(column), parse_literal());
    } while (accept_symbol(","));
    if (accept_kw("where")) {
      stmt.has_where = true;
      stmt.where = parse_condition();
    }
    expect_end();
    return stmt;
  }

  Statement parse_delete() {
    DeleteStmt stmt;
    expect_kw("from");
    stmt.table = expect_ident();
    if (accept_kw("where")) {
      stmt.has_where = true;
      stmt.where = parse_condition();
    }
    expect_end();
    return stmt;
  }

  std::vector<Token> tokens_;
  std::size_t pos_ = 0;
};

}  // namespace

Statement parse(std::string_view sql) {
  Parser parser(tokenize(sql));
  return parser.parse_statement();
}

}  // namespace mvdb::sql

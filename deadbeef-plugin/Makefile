# MMCP provider plugin for DeaDBeeF
#
# Usage:
#   make                          build mmcp.so
#   make DDB_INCLUDE=-I/path/to/deadbeef/include
#                                 build against specific deadbeef headers
#   make install PREFIX=~/.local  install into $PREFIX/lib/deadbeef
#
# If pkg-config knows about deadbeef, the include flags are picked up
# automatically.

CC ?= cc
PLUGIN := mmcp.so

PKGS := $(shell pkg-config --silence-errors --cflags deadbeef 2>/dev/null)
ifeq ($(strip $(PKGS)),)
    PKGS := $(DDB_INCLUDE)
endif

CFLAGS ?= -O2
CFLAGS += -std=gnu99 -Wall -Wextra -fPIC $(PKGS)
LDFLAGS += -shared

OBJS := mmcp.o websocket.o

all: $(PLUGIN)

$(PLUGIN): $(OBJS)
	$(CC) -o $@ $(OBJS) $(LDFLAGS)

%.o: %.c websocket.h
	$(CC) $(CFLAGS) -c -o $@ $<

clean:
	rm -f $(PLUGIN) $(OBJS)

# End-to-end test: builds a mock DeaDBeeF API harness, runs it against an
# in-test relay/controller (Go), and checks the full MMCP provider flow.
# Requires the DeaDBeeF source headers (DDB_INCLUDE or pkg-config) and Go.
DDB_HEADERS ?= $(shell pkg-config --variable=includedir deadbeef 2>/dev/null)/deadbeef

test: $(PLUGIN)
	mkdir -p /tmp/ddb-e2e
	git clone --depth 1 https://github.com/DeaDBeeF-Player/deadbeef /tmp/ddb-e2e/deadbeef-src 2>/dev/null || true
	$(CC) $(CFLAGS) -I$(DDB_HEADERS) -o /tmp/ddb-e2e/test_provider tests/test_provider.c mmcp.c websocket.c -lpthread
	cp tests/e2e_test.go /tmp/ddb-e2e/main.go
	cd /tmp/ddb-e2e && \
		[ -f go.mod ] || (go mod init ddbe2e && go mod edit -require github.com/gorilla/websocket@v1.5.3 && go mod tidy) && \
		go build -o e2e-test . && ./e2e-test /tmp/ddb-e2e/test_provider /tmp/ddb-e2e/events.log

install: $(PLUGIN)
	install -Dm755 $(PLUGIN) $(DESTDIR)$(if $(PREFIX),$(PREFIX),~/.local)/lib/deadbeef/$(PLUGIN)

.PHONY: all clean install test

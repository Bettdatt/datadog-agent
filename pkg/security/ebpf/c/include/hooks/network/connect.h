#ifndef _HOOKS_CONNECT_H_
#define _HOOKS_CONNECT_H_

#include "constants/offsets/netns.h"
#include "constants/syscall_macro.h"
#include "helpers/discarders.h"

HOOK_SYSCALL_ENTRY3(connect, int, socket, struct sockaddr *, addr, unsigned int, addr_len) {
    if (!addr) {
        return 0;
    }

    struct syscall_cache_t syscall = {
        .type = EVENT_CONNECT,
    };
    cache_syscall(&syscall);
    return 0;
}

int __attribute__((always_inline)) sys_connect_ret(void *ctx, int retval) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_CONNECT);
    if (!syscall) {
        return 0;
    }

    if (IS_UNHANDLED_ERROR(retval) && retval != -EINPROGRESS) {
        return 0;
    }

    /* pre-fill the event */
    struct connect_event_t event = {
        .syscall.retval = retval,
        .addr[0] = syscall->connect.addr[0],
        .addr[1] = syscall->connect.addr[1],
        .family = syscall->connect.family,
        .port = syscall->connect.port,
        .protocol = syscall->connect.protocol,
    };

    struct proc_cache_t *entry = fill_process_context(&event.process);
    fill_container_context(entry, &event.container);
    fill_span_context(&event.span);

    // Check if we should sample this event for activity dumps
    struct activity_dump_config *config = lookup_or_delete_traced_pid(event.process.pid, bpf_ktime_get_ns(), NULL);
    if (config) {
        if (mask_has_event(config->event_mask, EVENT_CONNECT)) {
            event.event.flags |= EVENT_FLAGS_ACTIVITY_DUMP_SAMPLE;
        }
    }

    send_event(ctx, EVENT_CONNECT, event);
    return 0;
}

HOOK_SYSCALL_EXIT(connect) {
    int retval = SYSCALL_PARMRET(ctx);
    return sys_connect_ret(ctx, retval);
}

HOOK_ENTRY("security_socket_connect")
int hook_security_socket_connect(ctx_t *ctx) {
    struct socket *sk = (struct socket *)CTX_PARM1(ctx);
    struct sockaddr *address = (struct sockaddr *)CTX_PARM2(ctx);
    short socket_type = 0;

    // fill syscall_cache if necessary
    struct syscall_cache_t *syscall = peek_syscall(EVENT_CONNECT);
    if (!syscall) {
        return 0;
    }

    // Extract IP and port from the sockaddr structure
    bpf_probe_read(&syscall->connect.family, sizeof(syscall->connect.family), &address->sa_family);

    if (syscall->connect.family == AF_INET) {
        struct sockaddr_in *addr_in = (struct sockaddr_in *)address;
        bpf_probe_read(&syscall->connect.port, sizeof(addr_in->sin_port), &addr_in->sin_port);
        bpf_probe_read(&syscall->connect.addr, sizeof(addr_in->sin_addr.s_addr), &addr_in->sin_addr.s_addr);
    } else if (syscall->connect.family == AF_INET6) {
        struct sockaddr_in6 *addr_in6 = (struct sockaddr_in6 *)address;
        bpf_probe_read(&syscall->connect.port, sizeof(addr_in6->sin6_port), &addr_in6->sin6_port);
        bpf_probe_read(&syscall->connect.addr, sizeof(u64) * 2, (char *)addr_in6 + offsetof(struct sockaddr_in6, sin6_addr));
    }

    bpf_probe_read(&socket_type, sizeof(socket_type), &sk->type);

    // We only handle TCP and UDP sockets for now
    if (socket_type == SOCK_STREAM) {
        syscall->connect.protocol = IPPROTO_TCP;
    } else if (socket_type == SOCK_DGRAM) {
        syscall->connect.protocol = IPPROTO_UDP;
    }
    return 0;
}

TAIL_CALL_TRACEPOINT_FNC(handle_sys_connect_exit, struct tracepoint_raw_syscalls_sys_exit_t *args) {
    return sys_connect_ret(args, args->ret);
}

#endif /* _CONNECT_H_ */

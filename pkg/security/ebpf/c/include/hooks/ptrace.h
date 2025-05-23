#ifndef _HOOKS_PTRACE_H_
#define _HOOKS_PTRACE_H_

#include "constants/syscall_macro.h"
#include "helpers/discarders.h"
#include "helpers/syscalls.h"

#define PTRACE_RATE_LIMITER_RATE 100

// list of requests we don't want to rate limit
static const int important_reqs[] = {
    PTRACE_ATTACH,
    PTRACE_DETACH,
    PTRACE_TRACEME,
    PTRACE_SEIZE,
    PTRACE_KILL,
    PTRACE_SETOPTIONS,
};

HOOK_SYSCALL_ENTRY3(ptrace, u32, request, pid_t, pid, void *, addr) {
    u8 found = 0;
    for (int i = 0; i < sizeof(important_reqs) / sizeof(int); i++) {
        if (request == important_reqs[i]) {
            found = 1;
            break;
        }
    }
    if (!found && !pid_rate_limiter_allow(PTRACE_RATE_LIMITER_RATE, 1)) {
        // for other requests types than a define list, rate limit the events
        return 0;
    }

    struct syscall_cache_t syscall = {
        .type = EVENT_PTRACE,
        .ptrace = {
            .request = request,
            .pid = 0, // 0 in case the root ns pid resolution failed
            .ns_pid = (u32)pid,
            .addr = (u64)addr,
        }
    };

    cache_syscall(&syscall);
    return 0;
}

int __attribute__((always_inline)) ptrace_check_attach_common(struct task_struct *child) {
    if (!child) {
        return 0;
    }

    struct syscall_cache_t *syscall = peek_syscall(EVENT_PTRACE);
    if (!syscall) {
        return 0;
    }

    // we already found the pid
    if (syscall->ptrace.pid != 0) {
        return 0;
    }
    syscall->ptrace.pid = get_root_nr_from_task_struct(child);

    return 0;
}

HOOK_ENTRY("ptrace_check_attach")
int hook_ptrace_check_attach(ctx_t *ctx) {
    return ptrace_check_attach_common((struct task_struct *)CTX_PARM1(ctx));
}

HOOK_ENTRY("arch_ptrace")
int hook_arch_ptrace(ctx_t *ctx) {
    return ptrace_check_attach_common((struct task_struct *)CTX_PARM1(ctx));
}

int __attribute__((always_inline)) sys_ptrace_ret(void *ctx, int retval) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_PTRACE);
    if (!syscall) {
        return 0;
    }

    struct ptrace_event_t event = {
        .syscall.retval = retval,
        .request = syscall->ptrace.request,
        .pid = syscall->ptrace.pid,
        .addr = syscall->ptrace.addr,
        .ns_pid = syscall->ptrace.ns_pid,
    };

    struct proc_cache_t *entry = fill_process_context(&event.process);
    fill_container_context(entry, &event.container);
    fill_span_context(&event.span);

    send_event(ctx, EVENT_PTRACE, event);
    return 0;
}

HOOK_SYSCALL_EXIT(ptrace) {
    return sys_ptrace_ret(ctx, (int)SYSCALL_PARMRET(ctx));
}

TAIL_CALL_TRACEPOINT_FNC(handle_sys_ptrace_exit, struct tracepoint_raw_syscalls_sys_exit_t *args) {
    return sys_ptrace_ret(args, args->ret);
}

#endif

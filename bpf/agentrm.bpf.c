// SPDX-License-Identifier: GPL-2.0
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

struct entity_state {
    __u64 used_ns;
    __u64 budget_ns;
    __u32 level;
    __u32 generation;
    __u64 reported;
};

struct threshold_event {
    __u64 cgroup_id;
    __u64 used_ns;
    __u64 budget_ns;
    __u64 timestamp_ns;
    __u32 level;
    __u32 generation;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u64);
    __type(value, struct entity_state);
} entities SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} last_switch_ns SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} events SEC(".maps");

SEC("tracepoint/sched/sched_switch")
int account_runtime(void *ctx)
{
    __u32 zero = 0;
    __u64 now = bpf_ktime_get_ns();
    __u64 *last = bpf_map_lookup_elem(&last_switch_ns, &zero);

    if (!last)
        return 0;

    if (*last != 0) {
        __u64 cgroup_id = bpf_get_current_cgroup_id();
        struct entity_state *state = bpf_map_lookup_elem(&entities, &cgroup_id);

        if (state && state->budget_ns != 0 && state->reported == 0) {
            __u64 delta = now - *last;
            __u64 before = __sync_fetch_and_add(&state->used_ns, delta);
            __u64 used = before + delta;

            if (before < state->budget_ns && used >= state->budget_ns &&
                __sync_bool_compare_and_swap(&state->reported, 0, 1)) {
                struct threshold_event *event;

                event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
                if (event) {
                    event->cgroup_id = cgroup_id;
                    event->used_ns = used;
                    event->budget_ns = state->budget_ns;
                    event->timestamp_ns = now;
                    event->level = state->level;
                    event->generation = state->generation;
                    bpf_ringbuf_submit(event, 0);
                } else {
                    /* Let a later switch retry if the ring buffer was full. */
                    __sync_bool_compare_and_swap(&state->reported, 1, 0);
                }
            }
        }
    }

    *last = now;
    return 0;
}

char LICENSE[] SEC("license") = "GPL";

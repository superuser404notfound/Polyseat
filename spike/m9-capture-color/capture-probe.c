// Does a wlroots scene-based capture source let a client ask for HDR?
//
// One process, forked. The parent is a headless wlroots compositor with a
// single output, a scene holding one rect of a known colour, and the two
// capture globals. The child is a capture client that opens a session on that
// output and takes exactly one frame.
//
// What is being measured is not "does capture work". It is what the session
// offers and what arrives in the buffer:
//
//   - which shm formats the session advertises, because eight bits a channel
//     cannot carry PQ and ten bits can
//   - the bytes of one pixel, because that is where a transfer function shows
//
// The source is chosen with PROBE_SOURCE: "scene" for the one from wlroots
// merge request 5443, which composites the scene a second time into a private
// output, and "raw" for the one that blits the real output's buffer and whose
// own header says it has no colour management.
//
// PROBE_HDR=1 asks the capture output for BT.2020 with the PQ transfer
// function. Upstream today has no way to ask that at all, so the run is
// expected to differ only once the patch under test is applied - a probe that
// printed the same thing either way would be measuring nothing.

#define _GNU_SOURCE

#include <assert.h>
#include <fcntl.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/wait.h>
#include <unistd.h>

#include <drm_fourcc.h>

#include <wayland-client.h>
#include <wayland-server-core.h>

#include <wlr/backend.h>
#include <wlr/backend/headless.h>
#include <wlr/render/allocator.h>
#include <wlr/render/wlr_renderer.h>
#include <wlr/types/wlr_compositor.h>
#include <wlr/types/wlr_ext_image_capture_source_v1.h>
#include <wlr/types/wlr_ext_image_copy_capture_v1.h>
#include <wlr/types/wlr_output.h>
#include <wlr/types/wlr_output_layout.h>
#include <wlr/types/wlr_scene.h>
#include <wlr/util/log.h>

#include "ext-image-capture-source-v1-client-protocol.h"
#include "ext-image-copy-capture-v1-client-protocol.h"

#define WIDTH 64
#define HEIGHT 64

// A mid grey. Deliberately not black or white: both survive almost any
// transfer function unchanged and would hide the very difference this looks
// for.
static const float RECT_COLOR[4] = {0.5f, 0.5f, 0.5f, 1.0f};

static bool want_hdr(void) {
	const char *s = getenv("PROBE_HDR");
	return s != NULL && strcmp(s, "1") == 0;
}

static bool want_scene_source(void) {
	const char *s = getenv("PROBE_SOURCE");
	return s == NULL || strcmp(s, "raw") != 0;
}

// ---------------------------------------------------------------- compositor

struct server {
	struct wl_display *display;
	struct wlr_scene_output *scene_output;
	struct wl_listener output_frame;
	struct wlr_backend *backend;
	struct wlr_renderer *renderer;
	struct wlr_allocator *allocator;
	struct wlr_scene *scene;
	struct wlr_output_layout *layout;
	struct wlr_output *output;
	struct wl_listener capture_request;
};

// The raw output source hands out the real output's own buffers, so it only
// produces a frame when that output commits one. Without this it waits
// forever and the run looks like a failure of the source rather than of the
// probe.
static void handle_output_frame(struct wl_listener *listener, void *data) {
	struct server *s = wl_container_of(listener, s, output_frame);
	wlr_scene_output_commit(s->scene_output, NULL);
}

static void handle_capture_request(struct wl_listener *listener, void *data) {
	struct server *s = wl_container_of(listener, s, capture_request);
	struct wlr_ext_output_image_capture_source_manager_v1_request_event *ev = data;

	struct wlr_ext_image_capture_source_v1 *source;
	if (want_scene_source()) {
		source = wlr_ext_image_capture_source_v1_create_with_scene_output(
			s->scene, ev->output, s->layout);
	} else {
		source = wlr_ext_image_capture_source_v1_create_with_raw_output(ev->output);
	}

	if (source == NULL) {
		fprintf(stderr, "compositor: could not create the capture source\n");
		return;
	}

	if (want_hdr()) {
		// The luminances are the PQ defaults: 10000 cd/m2 against a reference
		// of 203, which is the pair gamescope and Sunshine both compare.
		const struct wlr_output_image_description desc = {
			.primaries = WLR_COLOR_NAMED_PRIMARIES_BT2020,
			.transfer_function = WLR_COLOR_TRANSFER_FUNCTION_ST2084_PQ,
		};
		// PROBE_FORMAT because the right ten bit format is a property of the
		// driver rather than of the protocol: this card takes XB30 and
		// refuses XR30 at gbm_bo_create, though EGL lists both.
		const char *want = getenv("PROBE_FORMAT");
		uint32_t fmt = DRM_FORMAT_INVALID;
		if (want != NULL && strcmp(want, "XB30") == 0) {
			fmt = DRM_FORMAT_XBGR2101010;
		} else if (want != NULL && strcmp(want, "AB30") == 0) {
			fmt = DRM_FORMAT_ABGR2101010;
		} else if (want != NULL && strcmp(want, "XR30") == 0) {
			fmt = DRM_FORMAT_XRGB2101010;
		}

		if (!wlr_ext_image_capture_source_v1_set_image_description(source,
				&desc, fmt)) {
			fprintf(stderr, "compositor: this source will not take BT.2020 PQ\n");
		}
	}

	if (!wlr_ext_output_image_capture_source_manager_v1_request_accept(ev, source)) {
		fprintf(stderr, "compositor: the capture request was not accepted\n");
	}
}

static bool server_init(struct server *s) {
	s->display = wl_display_create();
	struct wl_event_loop *loop = wl_display_get_event_loop(s->display);

	s->backend = wlr_headless_backend_create(loop);
	if (s->backend == NULL) {
		return false;
	}

	s->renderer = wlr_renderer_autocreate(s->backend);
	if (s->renderer == NULL) {
		return false;
	}
	wlr_renderer_init_wl_display(s->renderer, s->display);

	s->allocator = wlr_allocator_autocreate(s->backend, s->renderer);
	if (s->allocator == NULL) {
		return false;
	}

	wlr_compositor_create(s->display, 6, s->renderer);

	s->scene = wlr_scene_create();
	s->layout = wlr_output_layout_create(s->display);

	s->output = wlr_headless_add_output(s->backend, WIDTH, HEIGHT);
	if (s->output == NULL) {
		return false;
	}
	wlr_output_init_render(s->output, s->allocator, s->renderer);

	struct wlr_output_state state;
	wlr_output_state_init(&state);
	wlr_output_state_set_enabled(&state, true);
	if (!wlr_output_commit_state(s->output, &state)) {
		wlr_output_state_finish(&state);
		return false;
	}
	wlr_output_state_finish(&state);

	wlr_scene_rect_create(&s->scene->tree, WIDTH, HEIGHT, RECT_COLOR);

	struct wlr_scene_output_layout *sol =
		wlr_scene_attach_output_layout(s->scene, s->layout);
	struct wlr_output_layout_output *lo =
		wlr_output_layout_add_auto(s->layout, s->output);
	struct wlr_scene_output *so = wlr_scene_output_create(s->scene, s->output);
	wlr_scene_output_layout_add_output(sol, lo, so);
	wlr_scene_output_commit(so, NULL);

	s->scene_output = so;
	s->output_frame.notify = handle_output_frame;
	wl_signal_add(&s->output->events.frame, &s->output_frame);

	wlr_output_create_global(s->output, s->display);

	struct wlr_ext_output_image_capture_source_manager_v1 *mgr =
		wlr_ext_output_image_capture_source_manager_v1_create(s->display, 1);
	s->capture_request.notify = handle_capture_request;
	wl_signal_add(&mgr->events.capture_request, &s->capture_request);

	wlr_ext_image_copy_capture_manager_v1_create(s->display, 1);

	return true;
}

// -------------------------------------------------------------------- client

struct client {
	struct wl_display *display;
	struct wl_registry *registry;
	struct wl_shm *shm;
	struct wl_output *output;
	struct ext_output_image_capture_source_manager_v1 *source_manager;
	struct ext_image_copy_capture_manager_v1 *capture_manager;

	uint32_t shm_formats[64];
	size_t num_shm_formats;

	uint32_t width, height;
	uint32_t formats[32];
	size_t num_formats;
	bool session_done;

	bool ready, failed;
	uint32_t fail_reason;

	void *data;
	size_t stride;
	uint32_t chosen_format;
};

static void shm_format(void *data, struct wl_shm *shm, uint32_t format) {
	struct client *c = data;
	if (c->num_shm_formats < sizeof(c->shm_formats) / sizeof(c->shm_formats[0])) {
		c->shm_formats[c->num_shm_formats++] = format;
	}
}

static const struct wl_shm_listener shm_listener = { .format = shm_format };

static void registry_global(void *data, struct wl_registry *registry,
		uint32_t name, const char *iface, uint32_t version) {
	struct client *c = data;

	if (strcmp(iface, wl_shm_interface.name) == 0) {
		c->shm = wl_registry_bind(registry, name, &wl_shm_interface, 1);
		wl_shm_add_listener(c->shm, &shm_listener, c);
	} else if (strcmp(iface, wl_output_interface.name) == 0 && c->output == NULL) {
		c->output = wl_registry_bind(registry, name, &wl_output_interface, 1);
	} else if (strcmp(iface, ext_output_image_capture_source_manager_v1_interface.name) == 0) {
		c->source_manager = wl_registry_bind(registry, name,
			&ext_output_image_capture_source_manager_v1_interface, 1);
	} else if (strcmp(iface, ext_image_copy_capture_manager_v1_interface.name) == 0) {
		c->capture_manager = wl_registry_bind(registry, name,
			&ext_image_copy_capture_manager_v1_interface, 1);
	}
}

static void registry_global_remove(void *data, struct wl_registry *r, uint32_t name) {
}

static const struct wl_registry_listener registry_listener = {
	.global = registry_global,
	.global_remove = registry_global_remove,
};

static void session_buffer_size(void *data,
		struct ext_image_copy_capture_session_v1 *session,
		uint32_t width, uint32_t height) {
	struct client *c = data;
	c->width = width;
	c->height = height;
}

static void session_shm_format(void *data,
		struct ext_image_copy_capture_session_v1 *session, uint32_t format) {
	struct client *c = data;
	if (c->num_formats < sizeof(c->formats) / sizeof(c->formats[0])) {
		c->formats[c->num_formats++] = format;
	}
}

static void session_dmabuf_device(void *data,
		struct ext_image_copy_capture_session_v1 *session, struct wl_array *dev) {
}

static void session_dmabuf_format(void *data,
		struct ext_image_copy_capture_session_v1 *session,
		uint32_t format, struct wl_array *modifiers) {
}

static void session_done(void *data,
		struct ext_image_copy_capture_session_v1 *session) {
	struct client *c = data;
	c->session_done = true;
}

static void session_stopped(void *data,
		struct ext_image_copy_capture_session_v1 *session) {
	struct client *c = data;
	c->failed = true;
	c->fail_reason = 0;
}

static const struct ext_image_copy_capture_session_v1_listener session_listener = {
	.buffer_size = session_buffer_size,
	.shm_format = session_shm_format,
	.dmabuf_device = session_dmabuf_device,
	.dmabuf_format = session_dmabuf_format,
	.done = session_done,
	.stopped = session_stopped,
};

static void frame_transform(void *data,
		struct ext_image_copy_capture_frame_v1 *frame, uint32_t transform) {
}

static void frame_damage(void *data,
		struct ext_image_copy_capture_frame_v1 *frame,
		int32_t x, int32_t y, int32_t width, int32_t height) {
}

static void frame_presentation_time(void *data,
		struct ext_image_copy_capture_frame_v1 *frame,
		uint32_t hi, uint32_t lo, uint32_t nsec) {
}

static void frame_ready(void *data, struct ext_image_copy_capture_frame_v1 *frame) {
	struct client *c = data;
	c->ready = true;
}

static void frame_failed(void *data,
		struct ext_image_copy_capture_frame_v1 *frame, uint32_t reason) {
	struct client *c = data;
	c->failed = true;
	c->fail_reason = reason;
}

static const struct ext_image_copy_capture_frame_v1_listener frame_listener = {
	.transform = frame_transform,
	.damage = frame_damage,
	.presentation_time = frame_presentation_time,
	.ready = frame_ready,
	.failed = frame_failed,
};

static const char *format_name(uint32_t f) {
	switch (f) {
	case WL_SHM_FORMAT_ARGB8888: return "ARGB8888";
	case WL_SHM_FORMAT_XRGB8888: return "XRGB8888";
	case WL_SHM_FORMAT_ABGR8888: return "ABGR8888";
	case WL_SHM_FORMAT_XBGR8888: return "XBGR8888";
	case WL_SHM_FORMAT_ARGB2101010: return "ARGB2101010";
	case WL_SHM_FORMAT_XRGB2101010: return "XRGB2101010";
	case WL_SHM_FORMAT_ABGR2101010: return "ABGR2101010";
	case WL_SHM_FORMAT_XBGR2101010: return "XBGR2101010";
	case WL_SHM_FORMAT_ARGB16161616F: return "ARGB16161616F";
	case WL_SHM_FORMAT_XRGB16161616F: return "XRGB16161616F";
	default: return NULL;
	}
}

static void print_format(uint32_t f) {
	const char *name = format_name(f);
	if (name != NULL) {
		printf(" %s", name);
	} else {
		printf(" 0x%08x", f);
	}
}

static bool is_ten_bit(uint32_t f) {
	switch (f) {
	case WL_SHM_FORMAT_ARGB2101010:
	case WL_SHM_FORMAT_XRGB2101010:
	case WL_SHM_FORMAT_ABGR2101010:
	case WL_SHM_FORMAT_XBGR2101010:
		return true;
	default:
		return false;
	}
}

static int run_client(const char *socket) {
	struct client c = {0};

	c.display = wl_display_connect(socket);
	if (c.display == NULL) {
		fprintf(stderr, "client: cannot connect to %s\n", socket);
		return 2;
	}

	c.registry = wl_display_get_registry(c.display);
	wl_registry_add_listener(c.registry, &registry_listener, &c);
	wl_display_roundtrip(c.display);

	if (c.shm == NULL || c.output == NULL ||
			c.source_manager == NULL || c.capture_manager == NULL) {
		fprintf(stderr, "client: missing globals (shm %p output %p source %p capture %p)\n",
			(void *)c.shm, (void *)c.output,
			(void *)c.source_manager, (void *)c.capture_manager);
		return 2;
	}

	struct ext_image_capture_source_v1 *source =
		ext_output_image_capture_source_manager_v1_create_source(
			c.source_manager, c.output);

	struct ext_image_copy_capture_session_v1 *session =
		ext_image_copy_capture_manager_v1_create_session(c.capture_manager,
			source, EXT_IMAGE_COPY_CAPTURE_MANAGER_V1_OPTIONS_PAINT_CURSORS);
	ext_image_copy_capture_session_v1_add_listener(session, &session_listener, &c);

	while (!c.session_done && !c.failed) {
		if (wl_display_dispatch(c.display) < 0) {
			fprintf(stderr, "client: disconnected before the session was ready\n");
			return 2;
		}
	}

	printf("  wl_shm has");
	bool shm_ten_bit = false;
	for (size_t i = 0; i < c.num_shm_formats; i++) {
		if (is_ten_bit(c.shm_formats[i])) {
			print_format(c.shm_formats[i]);
			shm_ten_bit = true;
		}
	}
	printf("%s\n", shm_ten_bit ? "" : " no ten bit format");

	printf("  buffer     %ux%u\n", c.width, c.height);
	printf("  shm formats");
	bool any_ten_bit = false;
	for (size_t i = 0; i < c.num_formats; i++) {
		print_format(c.formats[i]);
		any_ten_bit = any_ten_bit || is_ten_bit(c.formats[i]);
	}
	printf("\n");
	printf("  ten bit    %s\n", any_ten_bit ? "yes" : "no");

	if (c.num_formats == 0 || c.width == 0) {
		fprintf(stderr, "client: the session offered nothing to capture into\n");
		return 2;
	}

	// Ten bits where they are on offer, because that is the only way a PQ
	// encoded value survives the trip.
	c.chosen_format = c.formats[0];
	for (size_t i = 0; i < c.num_formats; i++) {
		if (is_ten_bit(c.formats[i])) {
			c.chosen_format = c.formats[i];
			break;
		}
	}

	c.stride = (size_t)c.width * 4;
	size_t size = c.stride * c.height;

	int fd = memfd_create("probe", MFD_CLOEXEC);
	if (fd < 0 || ftruncate(fd, (off_t)size) < 0) {
		fprintf(stderr, "client: no buffer\n");
		return 2;
	}
	c.data = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (c.data == MAP_FAILED) {
		fprintf(stderr, "client: cannot map the buffer\n");
		return 2;
	}
	memset(c.data, 0, size);

	struct wl_shm_pool *pool = wl_shm_create_pool(c.shm, fd, (int32_t)size);
	struct wl_buffer *buffer = wl_shm_pool_create_buffer(pool, 0,
		(int32_t)c.width, (int32_t)c.height, (int32_t)c.stride, c.chosen_format);
	wl_shm_pool_destroy(pool);

	struct ext_image_copy_capture_frame_v1 *frame =
		ext_image_copy_capture_session_v1_create_frame(session);
	ext_image_copy_capture_frame_v1_add_listener(frame, &frame_listener, &c);
	ext_image_copy_capture_frame_v1_attach_buffer(frame, buffer);
	ext_image_copy_capture_frame_v1_capture(frame);

	while (!c.ready && !c.failed) {
		if (wl_display_dispatch(c.display) < 0) {
			fprintf(stderr, "client: disconnected while waiting for the frame\n");
			return 2;
		}
	}

	if (c.failed) {
		printf("  frame      failed, reason %u\n", c.fail_reason);
		return 1;
	}

	printf("  captured  ");
	print_format(c.chosen_format);
	printf("\n");

	// The centre pixel, which is inside the rect on any size.
	const uint8_t *px = (const uint8_t *)c.data +
		(size_t)(c.height / 2) * c.stride + (size_t)(c.width / 2) * 4;
	uint32_t word;
	memcpy(&word, px, sizeof(word));

	if (is_ten_bit(c.chosen_format)) {
		unsigned r = (word >> 20) & 0x3ff;
		unsigned g = (word >> 10) & 0x3ff;
		unsigned b = word & 0x3ff;
		printf("  pixel      0x%08x  r=%u g=%u b=%u  (of 1023)\n", word, r, g, b);
	} else {
		unsigned r = (word >> 16) & 0xff;
		unsigned g = (word >> 8) & 0xff;
		unsigned b = word & 0xff;
		printf("  pixel      0x%08x  r=%u g=%u b=%u  (of 255)\n", word, r, g, b);
	}

	// What the three possible answers look like, so that a number is a
	// verdict rather than something to go and work out. The rect is mid grey;
	// only the transfer function moves it, because the primaries conversion
	// leaves a neutral alone.
	if (is_ten_bit(c.chosen_format)) {
		unsigned r = (word >> 20) & 0x3ff;
		printf("\n");
		printf("  nothing applied would be   512 of 1023\n");
		printf("  linear light would be      219 of 1023\n");
		printf("  BT.2020 PQ would be        437 of 1023\n");
		if (r > 420 && r < 455) {
			printf("  -> PQ, within rounding\n");
		} else {
			printf("  -> NOT PQ\n");
			return 1;
		}
	}

	return 0;
}

// ---------------------------------------------------------------------- main

int main(void) {
	wlr_log_init(getenv("PROBE_VERBOSE") ? WLR_DEBUG : WLR_ERROR, NULL);

	struct server s = {0};
	if (!server_init(&s)) {
		fprintf(stderr, "compositor: could not come up\n");
		return 2;
	}

	const char *socket = wl_display_add_socket_auto(s.display);
	if (socket == NULL) {
		fprintf(stderr, "compositor: no socket\n");
		return 2;
	}

	if (!wlr_backend_start(s.backend)) {
		fprintf(stderr, "compositor: the backend did not start\n");
		return 2;
	}

	printf("source     %s\n", want_scene_source() ? "scene (MR 5443)" : "raw output");
	printf("asked for  %s\n", want_hdr() ? "BT.2020 + ST2084 PQ" : "nothing, so sRGB");
	fflush(stdout);

	pid_t pid = fork();
	if (pid < 0) {
		fprintf(stderr, "fork failed\n");
		return 2;
	}

	if (pid == 0) {
		// The child talks to the socket the parent just opened. It inherits
		// the server's own file descriptors and touches none of them.
		//
		// _exit rather than return: the child also inherited the renderer's
		// GL context, and letting the driver run its atexit handlers in a
		// forked process hangs there forever. Measured, not feared.
		int rc = run_client(socket);
		// _exit does not flush, and everything this probe has to say went
		// through stdout.
		fflush(stdout);
		fflush(stderr);
		_exit(rc);
	}

	// The parent runs the compositor until the client is done with it. A
	// client that wedges would hang here, so the run is given a deadline by
	// whoever calls this, not by the compositor.
	struct wl_event_loop *loop = wl_display_get_event_loop(s.display);
	int status = 0;
	while (waitpid(pid, &status, WNOHANG) == 0) {
		wl_event_loop_dispatch(loop, 20);
		wl_display_flush_clients(s.display);
	}

	// The manager asserts that nothing is still listening when the display
	// goes away, and this listener outlives the client.
	wl_list_remove(&s.capture_request.link);
	wl_list_remove(&s.output_frame.link);

	wl_display_destroy_clients(s.display);
	wl_display_destroy(s.display);

	return WIFEXITED(status) ? WEXITSTATUS(status) : 2;
}

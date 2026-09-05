// Does a headless wlroots output admit to HDR?
//
// This is patch 1 measured rather than read. sway's output_supports_hdr() asks
// the output for BT.2020 primaries and the PQ transfer function and then asks
// the renderer for output colour transforms; the first two are what the patch
// supplies and what this checks. wlr_output_test_state() then covers both
// halves at once: output_basic_test() rejects an image description the output
// does not claim to support, and the headless backend's own output_test()
// rejects any state field outside its SUPPORTED_OUTPUT_STATE, which is the
// other line the patch touches.
//
// Exit 0 when the output does HDR, 1 when it does not, 2 when the probe could
// not run. Against an unpatched wlroots it must exit 1, and the check script
// runs it both ways for exactly that reason.
#include <stdio.h>
#include <wayland-server-core.h>
#include <wlr/backend/headless.h>
#include <wlr/render/color.h>
#include <wlr/types/wlr_output.h>
#include <wlr/util/log.h>

int main(void) {
	wlr_log_init(WLR_DEBUG, NULL);

	struct wl_display *display = wl_display_create();
	if (!display) { fprintf(stderr, "no display\n"); return 2; }

	struct wlr_backend *backend =
		wlr_headless_backend_create(wl_display_get_event_loop(display));
	if (!backend) { fprintf(stderr, "no headless backend\n"); return 2; }

	struct wlr_output *out = wlr_headless_add_output(backend, 1920, 1080);
	if (!out) { fprintf(stderr, "no headless output\n"); return 2; }

	printf("output %s: primaries 0x%x, transfer functions 0x%x\n",
		out->name, out->supported_primaries, out->supported_transfer_functions);

	int bt2020 = (out->supported_primaries & WLR_COLOR_NAMED_PRIMARIES_BT2020) != 0;
	int pq = (out->supported_transfer_functions & WLR_COLOR_TRANSFER_FUNCTION_ST2084_PQ) != 0;
	printf("  BT.2020 primaries:  %s\n", bt2020 ? "yes" : "no");
	printf("  ST2084 PQ transfer: %s\n", pq ? "yes" : "no");

	// Deliberately wlr_output_test_state and not wlr_output_state_set_image_description:
	// the setter is handed a state and never the output, so it cannot check
	// anything and always succeeds. Asking it would have looked like a test and
	// measured nothing.
	struct wlr_output_state state;
	wlr_output_state_init(&state);
	const struct wlr_output_image_description hdr = {
		.primaries = WLR_COLOR_NAMED_PRIMARIES_BT2020,
		.transfer_function = WLR_COLOR_TRANSFER_FUNCTION_ST2084_PQ,
	};
	// Enabled and with a mode in the same state, because wlroots refuses an
	// image description on a disabled output - "Tried to set image description
	// on a disabled output" - and a fresh headless output is disabled. Measured
	// the hard way: without these two lines this probe failed against the
	// patched build and would have read as the patch not working.
	wlr_output_state_set_enabled(&state, true);
	wlr_output_state_set_custom_mode(&state, 1920, 1080, 0);
	wlr_output_state_set_image_description(&state, &hdr);
	int accepted = wlr_output_test_state(out, &state);
	wlr_output_state_finish(&state);
	printf("  the output accepts an HDR state: %s\n", accepted ? "yes" : "no");

	return (bt2020 && pq && accepted) ? 0 : 1;
}

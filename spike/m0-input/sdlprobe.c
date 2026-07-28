/* sdlprobe.c - counts the gamepads SDL can see.
 *
 * This is the actual test for H6: the device node can be present inside the
 * container and still be invisible to games, because SDL enumerates through
 * libudev and no udev runs inside the container.
 *
 *   gcc -o sdlprobe sdlprobe.c $(pkg-config --cflags --libs sdl2)
 *
 * Exit 0 = at least one joystick detected, 2 = none, 1 = SDL error.
 */

#include <SDL2/SDL.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Watch mode: reports device changes for as long as it runs. This checks
 * whether a pad that arrives in the container only AFTER program start is
 * noticed at all - enumeration at startup and hotplug at runtime are two
 * different mechanisms. SDL listens for udev netlink uevents to do the
 * latter, and those normally never reach a container. For Steam, which runs
 * permanently, that is exactly the relevant case. */
static int watch(int seconds) {
    printf("watching for %d s ...\n", seconds);
    fflush(stdout);
    Uint32 start = SDL_GetTicks();
    int added = 0;
    while (SDL_GetTicks() - start < (Uint32)seconds * 1000) {
        SDL_Event ev;
        while (SDL_PollEvent(&ev)) {
            if (ev.type == SDL_JOYDEVICEADDED) {
                const char *n = SDL_JoystickNameForIndex(ev.jdevice.which);
                printf("  + added: index=%d  %s\n",
                       ev.jdevice.which, n ? n : "(unnamed)");
                fflush(stdout);
                added++;
            } else if (ev.type == SDL_JOYDEVICEREMOVED) {
                printf("  - removed: id=%d\n", ev.jdevice.which);
                fflush(stdout);
            }
        }
        SDL_Delay(100);
    }
    printf("Done. %d device(s) reported while watching.\n", added);
    return added;
}

int main(int argc, char **argv) {
    const char *disable_udev = SDL_getenv("SDL_JOYSTICK_DISABLE_UDEV");
    printf("SDL_JOYSTICK_DISABLE_UDEV = %s\n",
           disable_udev ? disable_udev : "(not set)");

    if (SDL_Init(SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER | SDL_INIT_EVENTS) != 0) {
        fprintf(stderr, "SDL_Init failed: %s\n", SDL_GetError());
        return 1;
    }

    if (argc > 1 && strcmp(argv[1], "--watch") == 0) {
        int seconds = (argc > 2) ? atoi(argv[2]) : 30;
        int added = watch(seconds);
        SDL_Quit();
        return added > 0 ? 0 : 2;
    }

    int n = SDL_NumJoysticks();
    printf("SDL sees %d joystick(s)\n", n);

    for (int i = 0; i < n; i++) {
        const char *name = SDL_JoystickNameForIndex(i);
        SDL_JoystickGUID guid = SDL_JoystickGetDeviceGUID(i);
        char guid_str[64];
        SDL_JoystickGetGUIDString(guid, guid_str, sizeof guid_str);
        printf("  [%d] %-40s  gamecontroller=%s  guid=%s\n",
               i, name ? name : "(unnamed)",
               SDL_IsGameController(i) ? "yes" : "no", guid_str);
    }

    SDL_Quit();
    return n > 0 ? 0 : 2;
}

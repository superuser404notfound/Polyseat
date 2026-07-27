/* sdlprobe.c — zählt, was SDL an Gamepads sieht.
 *
 * Das ist der eigentliche Test von H6: der Geräteknoten kann im Container
 * vorhanden sein und trotzdem für Spiele unsichtbar bleiben, weil SDL über
 * libudev enumeriert und im Container kein udev läuft.
 *
 *   gcc -o sdlprobe sdlprobe.c $(pkg-config --cflags --libs sdl2)
 *
 * Exit 0 = mindestens ein Joystick erkannt, 2 = keiner, 1 = SDL-Fehler.
 */

#include <SDL2/SDL.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Beobachtungsmodus: meldet Gerätewechsel, solange er läuft. Damit wird
 * geprüft, ob ein Pad, das ERST NACH dem Programmstart in den Container
 * kommt, überhaupt bemerkt wird — Enumeration beim Start und Hotplug zur
 * Laufzeit sind zwei verschiedene Mechanismen. SDL horcht dafür auf
 * udev-Netlink-Uevents, und die erreichen einen Container normalerweise
 * nicht. Für Steam, das dauerhaft läuft, ist genau das der relevante Fall. */
static int watch(int seconds) {
    printf("beobachte %d s ...\n", seconds);
    fflush(stdout);
    Uint32 start = SDL_GetTicks();
    int added = 0;
    while (SDL_GetTicks() - start < (Uint32)seconds * 1000) {
        SDL_Event ev;
        while (SDL_PollEvent(&ev)) {
            if (ev.type == SDL_JOYDEVICEADDED) {
                const char *n = SDL_JoystickNameForIndex(ev.jdevice.which);
                printf("  + hinzugefügt: index=%d  %s\n",
                       ev.jdevice.which, n ? n : "(namenlos)");
                fflush(stdout);
                added++;
            } else if (ev.type == SDL_JOYDEVICEREMOVED) {
                printf("  - entfernt: id=%d\n", ev.jdevice.which);
                fflush(stdout);
            }
        }
        SDL_Delay(100);
    }
    printf("Ende. %d Gerät(e) währenddessen gemeldet.\n", added);
    return added;
}

int main(int argc, char **argv) {
    const char *disable_udev = SDL_getenv("SDL_JOYSTICK_DISABLE_UDEV");
    printf("SDL_JOYSTICK_DISABLE_UDEV = %s\n",
           disable_udev ? disable_udev : "(nicht gesetzt)");

    if (SDL_Init(SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER | SDL_INIT_EVENTS) != 0) {
        fprintf(stderr, "SDL_Init fehlgeschlagen: %s\n", SDL_GetError());
        return 1;
    }

    if (argc > 1 && strcmp(argv[1], "--watch") == 0) {
        int seconds = (argc > 2) ? atoi(argv[2]) : 30;
        int added = watch(seconds);
        SDL_Quit();
        return added > 0 ? 0 : 2;
    }

    int n = SDL_NumJoysticks();
    printf("SDL sieht %d Joystick(s)\n", n);

    for (int i = 0; i < n; i++) {
        const char *name = SDL_JoystickNameForIndex(i);
        SDL_JoystickGUID guid = SDL_JoystickGetDeviceGUID(i);
        char guid_str[64];
        SDL_JoystickGetGUIDString(guid, guid_str, sizeof guid_str);
        printf("  [%d] %-40s  gamecontroller=%s  guid=%s\n",
               i, name ? name : "(namenlos)",
               SDL_IsGameController(i) ? "ja" : "nein", guid_str);
    }

    SDL_Quit();
    return n > 0 ? 0 : 2;
}

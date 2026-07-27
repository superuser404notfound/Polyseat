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

int main(void) {
    const char *disable_udev = SDL_getenv("SDL_JOYSTICK_DISABLE_UDEV");
    printf("SDL_JOYSTICK_DISABLE_UDEV = %s\n",
           disable_udev ? disable_udev : "(nicht gesetzt)");

    if (SDL_Init(SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER) != 0) {
        fprintf(stderr, "SDL_Init fehlgeschlagen: %s\n", SDL_GetError());
        return 1;
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
